# Code signing

Unsigned apps make Windows (SmartScreen) and macOS (Gatekeeper) warn users before the first start. The release workflow signs both platforms automatically as soon as the credentials below exist in the GitHub repository. Until then, releases stay unsigned and everything else works the same.

| Platform | Service | Cost | Configured by |
|---|---|---|---|
| Windows | [SignPath Foundation](https://signpath.org) (certificate issued to SignPath Foundation for open-source projects) | free for OSS | repository *variables* `SIGNPATH_*` + secret `SIGNPATH_API_TOKEN` |
| macOS | Apple Developer ID + notarization | Apple Developer Program membership (annual fee) | secrets `MACOS_*` and `APPLE_*` |

Signed binaries are used everywhere: in the GitHub release and in the Docker image, which serves them on the Devices page.

## Windows: SignPath Foundation

1. **Apply** at [signpath.org](https://signpath.org) with this repository. Requirements: an OSI-approved license (typehaus is AGPL-3.0), a public repository, and release builds on GitHub-hosted runners (already the case). Link the code signing policy: [`CODE_SIGNING_POLICY.md`](../CODE_SIGNING_POLICY.md).
2. **After approval**, in SignPath:
   - Create the project `typehaus` and link the *GitHub* trusted build system to it.
   - Artifact configuration: the workflow uploads a ZIP with the four `.exe` files. Sign all of them:
     ```xml
     <?xml version="1.0" encoding="utf-8"?>
     <artifact-configuration xmlns="http://signpath.io/artifact-configuration/v1">
       <zip-file>
         <pe-file-set>
           <include path="*.exe" max-matches="unbounded" />
           <for-each>
             <authenticode-sign />
           </for-each>
         </pe-file-set>
       </zip-file>
     </artifact-configuration>
     ```
   - Note the signing policy slug (typically `release-signing`) and create an API token for a CI user with *Submitter* permission.
3. **In GitHub** → *Settings → Secrets and variables → Actions*:
   - Variables: `SIGNPATH_ORGANIZATION_ID`, `SIGNPATH_PROJECT_SLUG`, `SIGNPATH_SIGNING_POLICY_SLUG`
   - Secret: `SIGNPATH_API_TOKEN`
4. Uncomment the SignPath line in `CODE_SIGNING_POLICY.md` and push a tag. Depending on the policy, approvers confirm each signing request in SignPath.

SmartScreen also considers the reputation of the certificate. SignPath Foundation's certificate is shared by many projects, which helps; brand-new certificates can still trigger warnings until downloads accumulate.

## macOS: Developer ID and notarization

1. Join the [Apple Developer Program](https://developer.apple.com/programs/) (paid, yearly).
2. In *Certificates, Identifiers & Profiles*, create a **Developer ID Application** certificate. Install it in Keychain Access and export it with its private key as `.p12` (set a password).
3. Create an **app-specific password** for your Apple ID at [account.apple.com](https://account.apple.com) → *Sign-In and Security*.
4. In GitHub → *Settings → Secrets and variables → Actions* → *Secrets*:

   | Secret | Value |
   |---|---|
   | `MACOS_CERTIFICATE_P12_BASE64` | `base64 -i DeveloperID.p12` (single line) |
   | `MACOS_CERTIFICATE_PASSWORD` | the `.p12` password |
   | `MACOS_SIGNING_IDENTITY` | e.g. `Developer ID Application: Dan Example (AB12CD34EF)` (`security find-identity -v -p codesigning`) |
   | `APPLE_ID` | your Apple ID e-mail |
   | `APPLE_TEAM_ID` | the 10-character team ID |
   | `APPLE_APP_PASSWORD` | the app-specific password |

The workflow signs with the hardened runtime and a secure timestamp, notarizes the app and the command-line binaries with `notarytool`, and staples the ticket to `typehaus.app`. With the certificate but without the `APPLE_*` secrets, apps are signed but not notarized, and Gatekeeper still warns.

## Checking a release

- Windows: right-click the `.exe` → *Properties → Digital Signatures*, or `Get-AuthenticodeSignature .\typehaus-agent-windows-amd64.exe`.
- macOS: `spctl --assess --type execute --verbose=2 /Applications/typehaus.app` should say `accepted, source=Notarized Developer ID`.
