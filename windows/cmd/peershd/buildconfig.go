package main

// Build-time defaults for Firebase / signaling secrets.
//
// Distribution model: a project owner running their own peershd build
// can ship binaries with their project values pre-baked, so end-user
// operators don't need to pass any -firebase-* / -google-* flags. The
// OSS source defaults to all empty values (no Firebase mode) so a
// vanilla `go build ./windows/cmd/peershd` is completely PSK-only.
//
// Override at build time:
//
//   go build -trimpath \
//     -ldflags "
//       -X 'main.embeddedFirebaseAPIKey=AIza...'
//       -X 'main.embeddedFirebaseProjectID=my-project'
//       -X 'main.embeddedFirebaseRegion=asia-northeast1'
//       -X 'main.embeddedSignalingURL=wss://signaling.example.com/ws'
//       -X 'main.embeddedGoogleClientID=...apps.googleusercontent.com'
//       -X 'main.embeddedGoogleClientSecret=GOCSPX-...'
//     " \
//     ./windows/cmd/peershd
//
// Embedded values become flag defaults; operators can still override
// any of them at runtime with the matching command-line flag.
//
// Security note: the Firebase Web API key and the "Desktop app" OAuth
// client secret are public-by-design (they identify the project, not a
// caller) — Google's docs explicitly say so. The Firebase project id
// and signaling URL are likewise non-sensitive. None of these variables
// should hold any value that would be dangerous in a binary an end user
// can grep through.
var (
	embeddedFirebaseAPIKey     string
	embeddedFirebaseProjectID  string
	embeddedFirebaseRegion     string
	embeddedSignalingURL       string
	embeddedGoogleClientID     string
	embeddedGoogleClientSecret string

	// Self-update bookkeeping. embeddedVersion is checked against the
	// latest release tag at update-time; embeddedUpdateRepo is the
	// "owner/repo" used to fetch GitHub release manifests. Empty repo
	// disables the update subcommand cleanly.
	embeddedVersion    = "dev"
	embeddedUpdateRepo string // e.g. "kamahei/peersh"
)
