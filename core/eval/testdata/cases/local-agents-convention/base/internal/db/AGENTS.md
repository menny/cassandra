# Database Convention
All database queries MUST use the `SafeQuery` wrapper, never the raw `sql.DB` directly. This ensures consistent error logging and metric collection.
