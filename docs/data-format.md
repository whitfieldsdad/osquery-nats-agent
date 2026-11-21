# Data format

Inbound messages, and the query requests in input files, are JSON objects containing the query and optional platform restrictions. Plain SQL is not accepted.

```json
{
    "query": "SELECT name, version FROM os_version;",
    "platforms": ["darwin", "linux", "windows"]
}
```

Outbound messages are JSON objects with the following structure. `date` and `time` are when the query finished, in UTC, using the same formats as the [`date` and `time` template properties](#time-based-template-properties).

```json
{
    "host": {
        "id": "dbdac958-5491-4b13-8c3d-784428fad940",
        "hostname": "lab-linux-1",
        "network_interfaces": [
            {
                "name": "eth0",
                "ip_addresses": ["127.0.0.1"],
                "mac_address": "00:00:00:00:00:00"
            }
        ],
        "platform": "linux"
    },
    "date": "2026-11-20",
    "time": "2026-11-20T12:34:56",
    "query": "SELECT name, version FROM os_version;",
    "error": null,
    "results": [
        {
            "name": "Ubuntu",
            "version": "26.04.1 LTS (Resolute Raccoon)"
        }
    ]
}
```

## Differential results

By default (`"snapshot": false`), every query's results are differential. The first time a query runs, `results` holds every row it returned. After that, each run reports only what changed since the query's previous successful run:

- `results` holds the rows that are new or changed.
- `removed` holds the rows that are gone. It is left out when empty.
- Rows are compared on all their columns, so a row whose values changed appears in both lists: the old version in `removed` and the new one in `results`.
- A run in which nothing changed sends no message.
- A failed run sends a message with `error` set and no rows. The next successful run is compared with the last successful one.
- Changes are delivered at least once. If a result cannot be written to every output file and published to every output subject, the next run reports its changes again, along with any new ones, so some destinations may see a change twice.

Each scheduled query is compared with its own previous run. Queries received over NATS or read from input files are compared with the previous run of the same SQL, wherever it came from. The agent keeps the previous results of the `max_cached_queries` (by default 10,000) most recently run of these queries; a query whose previous results were dropped reports all of its rows again. Previous results are kept in memory only, so after the agent restarts every query reports all of its rows again.

The agent logs how many rows changed on each run, e.g. `changes="5/20 rows changed (25%)"`. Changed rows are those added plus those removed; the total also counts the rows that were unchanged.

## Snapshot results

With `"snapshot": true` in the [agent configuration](agent-configuration.md), every run of every query reports all of the rows it returned, so each result is complete on its own:

- `results` holds every row the query returned. It is `[]` if there were none: a run that returns no rows still sends a message.
- `removed` is never sent.
- A failed run sends a message with `error` set and no rows.
- No previous results are kept, so `max_cached_queries` has no effect. A result that cannot be delivered to every output file and output subject is not sent again: the next run sends a newer snapshot.

## Large results

A result too large for one NATS message (the server's max payload, 1 MB by default) is split across several messages. Each is a complete result with some of the rows, so they can be processed independently. Output files always get the whole result in one record. A single row too large for a message on its own is left out of the published messages, and a message with an `error` naming the output files holding it is sent in its place.
