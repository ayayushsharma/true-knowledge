# Resident/socket tests fail on Android (Termux) — environment, not code

Date: 2026-10-11. Device: Termux, android/arm64.

`TestSilentClientDoesNotDelayAnUnrelatedRequest` (internal/resident),
`TestResidentAnswersWithoutSpawning`,
`TestResidentRefusalPropagatesAndDoesNotFallback`,
`TestResidentRefusalPropagatesOnJSONFace`,
`TestResidentLifecycleLogIsSeparateFromTrace`,
`TestInstallQuiescesAndResumesALiveResident` (internal/cli) fail on a CLEAN
checkout too (verified via `git stash`): "the resident never started
listening" / "resident never served on .../state/resident.sock" after 5s/10s
timeouts.

Real `tk mcp --detach` + dial works fine on the same device (proven same
day), so the resident itself is healthy — only the in-test serve path is
slow/broken here (possibly test socket setup vs Android loopback/abstract
sockets, or process-spawn timing under load). Do NOT "fix" by weakening
these tests; re-probe on Linux/macOS and on-device after Termux changes.
