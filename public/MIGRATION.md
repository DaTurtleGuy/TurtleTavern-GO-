# Migrating from TurtleTavern (Node.js) to TurtleTavern (Go)

The Node.js version is deprecated. The Go server has feature parity
(including per-user backup/restore) and archives are wire-compatible both
ways, so your data moves with one export/import cycle.

## Steps

1. **Back up in the old (Node) server**
   - Toolbar -> User Settings -> Account -> **Download Backup**
   - (Admins can back up other users from the user list)
   - Keep the downloaded `<handle>-<timestamp>.zip`.

2. **Start the Go server** (see the download that ships `gotavern.exe`
   for Windows or `gotavern` + Termux notes for Android)

3. **Import the backup into the Go server**
   - Toolbar -> User Settings -> Account -> **Restore Backup**
   - Choose the zip. The server validates `manifest.json` and every
     file's SHA-256, stages the files, then swaps them in atomically
     with rollback on failure.

4. The old Node install can be removed after you confirm everything
   (settings, chats, characters) is present.

## Format notes for future tooling

- zip contains the user's data root; then `checksums.sha256`
  (`<64 hex sha256><2 spaces><relative path>` lines); `manifest.json` last
- manifest: format `turtletavern-backup`, `formatVersion` 1
- secrets.json only included when the server's `allowKeysExposure` is true
