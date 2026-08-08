package lyricssource

import (
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestMoegirlPageURLTargetForURLAcceptsOnlyCanonicalPublicArticle(t *testing.T) {
	const pageURL = "https://zh.moegirl.org.cn/%E4%BA%BF%E5%B9%B4%E7%88%B1%E6%81%8B"
	target, err := MoegirlPageURLTargetForURL(pageURL)
	if err != nil {
		t.Fatal(err)
	}
	if target.PageURL != pageURL || target.PageTitle != "亿年爱恋" {
		t.Fatalf("Moegirl exact URL target=%+v", target)
	}
	for _, invalid := range []string{
		"http://zh.moegirl.org.cn/%E4%BA%BF%E5%B9%B4%E7%88%B1%E6%81%8B",
		"https://moegirl.icu/%E4%BA%BF%E5%B9%B4%E7%88%B1%E6%81%8B",
		pageURL + "?oldid=1",
		pageURL + "#lyrics",
		"https://zh.moegirl.org.cn/a/b",
		"https://zh.moegirl.org.cn/亿年爱恋",
	} {
		if _, err := MoegirlPageURLTargetForURL(invalid); err == nil {
			t.Fatalf("invalid Moegirl public URL %q was accepted", invalid)
		}
	}
}

func TestPreflightMoegirlPageURLRequestsExactFullURL(t *testing.T) {
	const pageURL = "https://zh.moegirl.org.cn/%E4%BA%BF%E5%B9%B4%E7%88%B1%E6%81%8B"
	target, err := MoegirlPageURLTargetForURL(pageURL)
	if err != nil {
		t.Fatal(err)
	}
	client := &http.Client{
		Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
			if request.URL.String() != pageURL || request.Header.Get("Accept") != "text/html,application/xhtml+xml" {
				t.Fatalf("direct request URL=%q headers=%v", request.URL, request.Header)
			}
			return &http.Response{
				StatusCode: http.StatusOK,
				Status:     "200 OK",
				Header:     http.Header{"Content-Type": []string{"text/html; charset=utf-8"}},
				Body:       io.NopCloser(strings.NewReader("<html>lyrics</html>")),
				Request:    request,
			}, nil
		}),
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	status, batch, err := PreflightMoegirlPageURL(t.Context(), target, client, 1, 0)
	if err != nil {
		t.Fatal(err)
	}
	if status.PageURL != pageURL || status.FinalURL != pageURL || status.StatusCode != http.StatusOK ||
		status.ContentType != "text/html" || status.ContentBytes != len("<html>lyrics</html>") || status.Redirected ||
		batch.RequestURL != pageURL || string(batch.Raw) != "<html>lyrics</html>" {
		t.Fatalf("direct status=%+v batch=%+v", status, batch)
	}
}
