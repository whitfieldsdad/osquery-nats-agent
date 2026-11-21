# TODO

- Add support for listing installed software
- Add support for scanning files with YARA
- Add support for real-time event tracing
- Add support for auto-clustering of agents
- Add embedded NATS server support
- Add support for BLAKE2, BLAKE3 hashing algorithms
- Add Python script for continuously monitoring query results in real-time
- Add support for osquery's differential query format
- Use JCS with SHA-256 or BLAKE3 to detect when query results have changed
- Write one-liner to deploy the agent with a configuration file
- Write Python script to export query results from NATS to files
- Add support for virtual filesystems (e.g. for storing YARA rules)
- Add support for "worming" over SSH with a set of static credentials or SSH keys and either statically defined hosts or dynamically discovered hosts (e.g. by checking the ARP cache for nearby hosts or performing a ping sweep)
- Add support for kill switches to remotely kill agents (e.g. using the NATS key-value store to kill agents with specific IDs)
- Gracefully handle inaccessible tables - log an error or fail silently in the background (since you can't change what you can't change)
- Add option to automatically drop null columns from query results (i.e. per response)
- Add option to automatically drop null fields from query results (i.e. per row)
