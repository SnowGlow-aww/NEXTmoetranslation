package main

func productionPolicy() launcherPolicy {
	return launcherPolicy{
		GOOS:             "darwin",
		GOARCH:           "arm64",
		WorkingDirectory: "/private/tmp/moesekai-704-recovery-v2",
		Runbook: filePin{
			Path:   "/private/tmp/moesekai-704-recovery-v2/run-704-recovery-v2-trust-v1.sh",
			SHA256: "a63f38b493e2d6c1638238582ed54d06108d041cdbcf4765d1d76191908eb9f7",
		},
		RunbookAncestry: []directoryPin{
			{Path: "/"},
			{Path: "/private"},
			{Path: "/private/tmp"},
			{Path: "/private/tmp/moesekai-704-recovery-v2"},
		},
		Bash: filePin{
			Path:   "/bin/bash",
			SHA256: "fde343ee184953c1fa1185abddeaa8be61c6acbebae4eb54db5d6b55b09a5755",
		},
		BashAncestry: []directoryPin{
			{Path: "/"},
			{Path: "/bin"},
		},
	}
}
