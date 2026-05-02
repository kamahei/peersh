// Mutable flag set once at app startup. Lives in its own file so the
// `flavor.dart` getter stays read-only at the import site.

bool firebaseInitialized = false;
