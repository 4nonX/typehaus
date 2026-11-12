# Code signing policy

typehaus release binaries are built from this public repository by GitHub Actions on GitHub-hosted runners (`.github/workflows/release.yml`) and signed in that pipeline. Nothing is signed from a developer's machine.

**Windows:** <!-- Free code signing provided by [SignPath.io](https://signpath.io), certificate by [SignPath Foundation](https://signpath.org). -->
Signing via SignPath Foundation has been applied for. Until it is active, Windows binaries are unsigned.

**macOS:** signed with an Apple Developer ID and notarized by Apple once the project's Developer ID is configured; until then ad-hoc signed.

## What is signed

The release artifacts of this repository: `typehaus-agent-*.exe` / `typehaus-agentw-*.exe` (Windows), `typehaus.app` and `typehaus-agent-darwin-*` (macOS). No third-party binaries are signed.

## Roles

- Committers and reviewers: [repository collaborators](https://github.com/4nonX/typehaus/graphs/contributors)
- Approvers: [@4nonX](https://github.com/4nonX)

Every release is created from a version tag on `main`. Changes reach `main` through pull requests.

## Privacy

typehaus agents connect only to the typehaus server the user or administrator configured, and only to sync the fonts assigned to that user. They don't send telemetry or contact any other service. The server software sends no data to the typehaus project or any third party.
