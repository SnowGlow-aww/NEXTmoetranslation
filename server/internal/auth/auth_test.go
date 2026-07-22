package auth

import (
	"sync"
	"testing"
	"time"

	"moesekai/server/internal/db"
)

func openTestAuth(t *testing.T) *Auth {
	t.Helper()
	database, err := db.Open(t.TempDir() + "/auth.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	return New(database, "jwt-secret", time.Hour)
}

func TestCreateAndAuthenticate(t *testing.T) {
	a := openTestAuth(t)
	if _, err := a.CreateUser("alice", "pw123", RoleAdmin); err != nil {
		t.Fatal(err)
	}
	u, err := a.Authenticate("alice", "pw123")
	if err != nil {
		t.Fatalf("authenticate: %v", err)
	}
	if u.Role != RoleAdmin {
		t.Errorf("role: got %q", u.Role)
	}
	if _, err := a.Authenticate("alice", "wrong"); err != ErrInvalidCreds {
		t.Errorf("expected ErrInvalidCreds, got %v", err)
	}
}

func TestDuplicateUser(t *testing.T) {
	a := openTestAuth(t)
	a.CreateUser("bob", "pw", RoleEditor)
	if _, err := a.CreateUser("bob", "pw2", RoleEditor); err != ErrUserExists {
		t.Errorf("expected ErrUserExists, got %v", err)
	}
}

func TestJWTRoundTrip(t *testing.T) {
	a := openTestAuth(t)
	u, _ := a.CreateUser("carol", "pw", RoleEditor)
	token, _, err := a.IssueToken(u)
	if err != nil {
		t.Fatal(err)
	}
	claims, err := a.VerifyToken(token)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if claims.Username != "carol" || claims.Role != RoleEditor {
		t.Errorf("claims mismatch: %+v", claims)
	}
	if _, err := a.VerifyToken("garbage.token.here"); err == nil {
		t.Error("expected error for garbage token")
	}
}

func TestExpiredToken(t *testing.T) {
	database, _ := db.Open(t.TempDir() + "/exp.db")
	defer database.Close()
	a := New(database, "s", time.Hour)
	a.tokenTTL = -time.Hour // force already-expired tokens (bypasses New's clamp)
	u, _ := a.CreateUser("dave", "pw", RoleEditor)
	token, _, _ := a.IssueToken(u)
	if _, err := a.VerifyToken(token); err == nil {
		t.Error("expected expired token to fail verification")
	}
}

func TestLastAdminGuard(t *testing.T) {
	a := openTestAuth(t)
	a.CreateUser("admin1", "pw", RoleAdmin)
	a.CreateUser("editor1", "pw", RoleEditor)

	// Demoting the only admin must fail.
	if err := a.SetRole("admin1", RoleEditor); err != ErrLastAdmin {
		t.Errorf("expected ErrLastAdmin on demote, got %v", err)
	}
	// Deleting the only admin must fail.
	if err := a.DeleteUser("admin1"); err != ErrLastAdmin {
		t.Errorf("expected ErrLastAdmin on delete, got %v", err)
	}
	// With a second admin, demotion is allowed.
	a.CreateUser("admin2", "pw", RoleAdmin)
	if err := a.SetRole("admin1", RoleEditor); err != nil {
		t.Errorf("demote with 2 admins should succeed: %v", err)
	}
}

func TestWrongSecretRejected(t *testing.T) {
	a := openTestAuth(t)
	u, _ := a.CreateUser("eve", "pw", RoleEditor)
	token, _, _ := a.IssueToken(u)
	other := New(a.db, "different-secret", time.Hour)
	if _, err := other.VerifyToken(token); err == nil {
		t.Error("token signed with one secret must not verify under another")
	}
}

func TestCreateFirstAdminIsAtomicUnderConcurrency(t *testing.T) {
	a := openTestAuth(t)
	start := make(chan struct{})
	results := make(chan error, 2)
	var wait sync.WaitGroup
	for _, username := range []string{"first", "second"} {
		wait.Add(1)
		go func(username string) {
			defer wait.Done()
			<-start
			_, err := a.CreateFirstAdmin(username, "password")
			results <- err
		}(username)
	}
	close(start)
	wait.Wait()
	close(results)
	succeeded, completed := 0, 0
	for err := range results {
		switch err {
		case nil:
			succeeded++
		case ErrSetupComplete:
			completed++
		default:
			t.Fatalf("concurrent setup error = %v", err)
		}
	}
	count, err := a.CountUsers()
	if err != nil {
		t.Fatal(err)
	}
	if succeeded != 1 || completed != 1 || count != 1 {
		t.Fatalf("setup success=%d complete=%d users=%d", succeeded, completed, count)
	}
}

func TestRolePasswordAndDeleteRevokeIssuedTokens(t *testing.T) {
	a := openTestAuth(t)
	admin, err := a.CreateUser("admin", "old-password", RoleAdmin)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.CreateUser("backup-admin", "password", RoleAdmin); err != nil {
		t.Fatal(err)
	}
	roleToken, _, err := a.IssueToken(admin)
	if err != nil {
		t.Fatal(err)
	}
	if err := a.SetRole("admin", RoleEditor); err != nil {
		t.Fatal(err)
	}
	if _, err := a.VerifyToken(roleToken); err != ErrInvalidCreds {
		t.Fatalf("demoted admin token error = %v", err)
	}

	current, err := a.Authenticate("admin", "old-password")
	if err != nil || current.Role != RoleEditor {
		t.Fatalf("current demoted user = %+v err=%v", current, err)
	}
	passwordToken, _, err := a.IssueToken(current)
	if err != nil {
		t.Fatal(err)
	}
	if err := a.SetPassword("admin", "new-password"); err != nil {
		t.Fatal(err)
	}
	if _, err := a.VerifyToken(passwordToken); err != ErrInvalidCreds {
		t.Fatalf("password-reset token error = %v", err)
	}
	if _, err := a.Authenticate("admin", "old-password"); err != ErrInvalidCreds {
		t.Fatalf("old password error = %v", err)
	}

	if err := a.SetRole("admin", RoleAdmin); err != nil {
		t.Fatal(err)
	}
	current, err = a.Authenticate("admin", "new-password")
	if err != nil {
		t.Fatal(err)
	}
	deleteToken, _, err := a.IssueToken(current)
	if err != nil {
		t.Fatal(err)
	}
	if err := a.DeleteUser("admin"); err != nil {
		t.Fatal(err)
	}
	if _, err := a.VerifyToken(deleteToken); err != ErrInvalidCreds {
		t.Fatalf("deleted admin token error = %v", err)
	}
}

func TestRefreshTokenCannotBeReplayed(t *testing.T) {
	a := openTestAuth(t)
	user, err := a.CreateUser("editor", "password", RoleEditor)
	if err != nil {
		t.Fatal(err)
	}
	token, _, err := a.IssueToken(user)
	if err != nil {
		t.Fatal(err)
	}
	claims, err := a.VerifyToken(token)
	if err != nil {
		t.Fatal(err)
	}
	replacement, _, err := a.RefreshToken(claims)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.VerifyToken(token); err != ErrInvalidCreds {
		t.Fatalf("consumed token verification error = %v", err)
	}
	if _, _, err := a.RefreshToken(claims); err != ErrInvalidCreds {
		t.Fatalf("sequential refresh replay error = %v", err)
	}
	replacementClaims, err := a.VerifyToken(replacement)
	if err != nil {
		t.Fatal(err)
	}
	if replacementClaims.TokenVersion != claims.TokenVersion+1 {
		t.Fatalf("replacement version = %d, want %d", replacementClaims.TokenVersion, claims.TokenVersion+1)
	}
}

func TestConcurrentRefreshTokenDoubleSpendAllowsOneReplacement(t *testing.T) {
	a := openTestAuth(t)
	user, err := a.CreateUser("editor", "password", RoleEditor)
	if err != nil {
		t.Fatal(err)
	}
	token, _, err := a.IssueToken(user)
	if err != nil {
		t.Fatal(err)
	}
	claims, err := a.VerifyToken(token)
	if err != nil {
		t.Fatal(err)
	}
	type result struct {
		token string
		err   error
	}
	start := make(chan struct{})
	results := make(chan result, 2)
	var wait sync.WaitGroup
	for range 2 {
		wait.Add(1)
		go func() {
			defer wait.Done()
			<-start
			replacement, _, err := a.RefreshToken(claims)
			results <- result{token: replacement, err: err}
		}()
	}
	close(start)
	wait.Wait()
	close(results)
	succeeded, rejected := 0, 0
	var replacement string
	for result := range results {
		switch result.err {
		case nil:
			succeeded++
			replacement = result.token
		case ErrInvalidCreds:
			rejected++
		default:
			t.Fatalf("concurrent refresh error = %v", result.err)
		}
	}
	if succeeded != 1 || rejected != 1 {
		t.Fatalf("concurrent refresh success=%d rejected=%d", succeeded, rejected)
	}
	replacementClaims, err := a.VerifyToken(replacement)
	if err != nil {
		t.Fatal(err)
	}
	if replacementClaims.TokenVersion != claims.TokenVersion+1 {
		t.Fatalf("replacement version = %d, want %d", replacementClaims.TokenVersion, claims.TokenVersion+1)
	}
}

func TestRefreshTokenIsAtomicWithRevocation(t *testing.T) {
	a := openTestAuth(t)
	admin, err := a.CreateUser("admin", "password", RoleAdmin)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.CreateUser("backup-admin", "password", RoleAdmin); err != nil {
		t.Fatal(err)
	}
	token, _, err := a.IssueToken(admin)
	if err != nil {
		t.Fatal(err)
	}
	claims, err := a.VerifyToken(token)
	if err != nil {
		t.Fatal(err)
	}
	validated := make(chan struct{})
	release := make(chan struct{})
	refreshTokenValidatedHook = func() {
		close(validated)
		<-release
	}
	t.Cleanup(func() { refreshTokenValidatedHook = nil })
	type refreshResult struct {
		token string
		err   error
	}
	refreshed := make(chan refreshResult, 1)
	go func() {
		newToken, _, err := a.RefreshToken(claims)
		refreshed <- refreshResult{token: newToken, err: err}
	}()
	<-validated
	revoked := make(chan error, 1)
	go func() { revoked <- a.SetRole("admin", RoleEditor) }()
	select {
	case err := <-revoked:
		t.Fatalf("revocation crossed active refresh transaction: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	close(release)
	result := <-refreshed
	if result.err != nil {
		t.Fatal(result.err)
	}
	if err := <-revoked; err != nil {
		t.Fatal(err)
	}
	if _, err := a.VerifyToken(result.token); err != ErrInvalidCreds {
		t.Fatalf("refreshed pre-revocation generation error = %v", err)
	}

	current, err := a.Authenticate("admin", "password")
	if err != nil {
		t.Fatal(err)
	}
	currentToken, _, err := a.IssueToken(current)
	if err != nil {
		t.Fatal(err)
	}
	staleClaims, err := a.VerifyToken(currentToken)
	if err != nil {
		t.Fatal(err)
	}
	if err := a.SetPassword("admin", "new-password"); err != nil {
		t.Fatal(err)
	}
	refreshTokenValidatedHook = nil
	if _, _, err := a.RefreshToken(staleClaims); err != ErrInvalidCreds {
		t.Fatalf("refresh after prior revocation error = %v", err)
	}
}

func TestConcurrentAdminMutationsCannotRemoveEveryAdmin(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*Auth, string) error
	}{
		{name: "demote", mutate: func(a *Auth, username string) error { return a.SetRole(username, RoleEditor) }},
		{name: "delete", mutate: func(a *Auth, username string) error { return a.DeleteUser(username) }},
	} {
		t.Run(test.name, func(t *testing.T) {
			a := openTestAuth(t)
			for _, username := range []string{"admin-a", "admin-b"} {
				if _, err := a.CreateUser(username, "password", RoleAdmin); err != nil {
					t.Fatal(err)
				}
			}
			start := make(chan struct{})
			results := make(chan error, 2)
			var wait sync.WaitGroup
			for _, username := range []string{"admin-a", "admin-b"} {
				wait.Add(1)
				go func(username string) {
					defer wait.Done()
					<-start
					results <- test.mutate(a, username)
				}(username)
			}
			close(start)
			wait.Wait()
			close(results)
			succeeded, protected := 0, 0
			for err := range results {
				switch err {
				case nil:
					succeeded++
				case ErrLastAdmin:
					protected++
				default:
					t.Fatalf("concurrent %s error = %v", test.name, err)
				}
			}
			var adminCount int
			if err := a.db.QueryRow(`SELECT COUNT(*) FROM users WHERE role=?`, RoleAdmin).Scan(&adminCount); err != nil {
				t.Fatal(err)
			}
			if succeeded != 1 || protected != 1 || adminCount != 1 {
				t.Fatalf("%s success=%d protected=%d admins=%d", test.name, succeeded, protected, adminCount)
			}
		})
	}
}
