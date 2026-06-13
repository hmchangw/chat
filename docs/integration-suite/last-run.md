# Integration tests — total   (latest merge: d225aa9f)

Run:        2026-06-06T21:54:11Z   (runID 1132)
Duration:   165ms
Total:      2 test cases

## Confusion matrix

                        true (pass)   false (fail)
  +ve (through)         0             2            
  -ve (error/warning)   0             0            

## Status breakdown

              pass    fail
APPROVED      0       0      
DRAFT         0       2      

## Cases

| case | latest | best | worst | reads | cascades | duration |
|------|--------|------|-------|-------|----------|----------|
| room-create-federates-cross-site/stub | fail | fail | fail | — | 0 | 0ms |
| room-create-federates-cross-site[c=0 p=- x=-] | fail | fail | fail | — | 0 | 0ms |
| room-creates-federates-to-site-b/stub | fail | fail | fail | — | 0 | 0ms |
| room-creates-federates-to-site-b[c=0 p=- x=-] | fail | fail | fail | — | 0 | 0ms |

## Failure Details

### room-create-federates-cross-site — fail

- subset: `scenario`  kind: `positive`  duration: 0ms
- reason:

```
sandbox setup: sandbox.Setup: truncate cassandra thread_messages_by_room: table thread_messages_by_room does not exist
```

### room-creates-federates-to-site-b — fail

- subset: `scenario`  kind: `positive`  duration: 0ms
- reason:

```
sandbox setup: sandbox.Setup: truncate cassandra thread_messages_by_room: table thread_messages_by_room does not exist
```

