package main

func productionPolicy() launcherPolicy {
	return launcherPolicy{
		GOOS:             "darwin",
		GOARCH:           "arm64",
		ExpectedEUID:     501,
		WorkingDirectory: "/private/tmp/moesekai-704-recovery-v2",
		Runbook: filePin{
			Path:   "/private/tmp/moesekai-704-recovery-v2/run-704-recovery-v2-trust-v1.sh",
			SHA256: "a63f38b493e2d6c1638238582ed54d06108d041cdbcf4765d1d76191908eb9f7",
			Identity: objectIdentity{
				Device:         16777229,
				Inode:          143659643,
				UID:            501,
				GID:            0,
				Mode:           0o700,
				LinkCount:      1,
				Size:           31152,
				ModificationNS: 1785649255244108787,
			},
		},
		RunbookAncestry: []directoryPin{
			{
				Path: "/",
				Identity: objectIdentity{
					Device: 16777229,
					Inode:  2,
					UID:    0,
					GID:    0,
					Mode:   0o755,
				},
			},
			{
				Path: "/private",
				Identity: objectIdentity{
					Device: 16777229,
					Inode:  128986443,
					UID:    0,
					GID:    0,
					Mode:   0o755,
				},
			},
			{
				Path: "/private/tmp",
				Identity: objectIdentity{
					Device: 16777229,
					Inode:  132222121,
					UID:    0,
					GID:    0,
					Mode:   0o1777,
				},
			},
			{
				Path: "/private/tmp/moesekai-704-recovery-v2",
				Identity: objectIdentity{
					Device: 16777229,
					Inode:  143100522,
					UID:    501,
					GID:    0,
					Mode:   0o700,
				},
			},
		},
		Bash: filePin{
			Path:   "/bin/bash",
			SHA256: "fde343ee184953c1fa1185abddeaa8be61c6acbebae4eb54db5d6b55b09a5755",
			Identity: objectIdentity{
				Device:         16777229,
				Inode:          1152921500312571378,
				UID:            0,
				GID:            0,
				Mode:           0o555,
				LinkCount:      1,
				Size:           1293840,
				ModificationNS: 1782354543000000000,
			},
		},
		BashAncestry: []directoryPin{
			{
				Path: "/",
				Identity: objectIdentity{
					Device: 16777229,
					Inode:  2,
					UID:    0,
					GID:    0,
					Mode:   0o755,
				},
			},
			{
				Path: "/bin",
				Identity: objectIdentity{
					Device: 16777229,
					Inode:  1152921500312571375,
					UID:    0,
					GID:    0,
					Mode:   0o755,
				},
			},
		},
	}
}
