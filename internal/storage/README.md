# Storage

The first verified implementation will use SQLite WAL for both manager and client state. This milestone only commits the initial schema draft so the table boundaries can be reviewed before Go SQLite wiring is added.

No migration runner is enabled yet because the current workstation does not have Go or a SQLite driver available for verified builds.

