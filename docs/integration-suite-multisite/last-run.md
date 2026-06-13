# Integration tests — total   (latest merge: d225aa9f)

Run:        2026-06-13T23:23:45Z   (runID a13d)
Duration:   1.152s
Total:      3 test cases

## Confusion matrix

                        true (pass)   false (fail)
  +ve (through)         3             0            
  -ve (error/warning)   0             0            

## Status breakdown

              pass    fail
APPROVED      0       0      
DRAFT         3       0      

## Cases

| case | latest | best | worst | reads | cascades | duration |
|------|--------|------|-------|-------|----------|----------|
| create-then-rename-after-room-persists/create | pass | pass | pass | — | 0 | 2ms |
| create-then-rename-after-room-persists/create_accepted | pass | pass | fail | — | 0 | 100ms |
| create-then-rename-after-room-persists/rename | pass | pass | halted-upstream | — | 0 | 1ms |
| create-then-rename-after-room-persists/rename_accepted | pass | pass | halted-upstream | — | 0 | 0ms |
| create-then-rename-after-room-persists/rename_canonical | pass | pass | halted-upstream | — | 0 | 101ms |
| create-then-rename-after-room-persists/room_persisted | pass | pass | halted-upstream | — | 0 | 0ms |
| create-then-rename-after-room-persists/room_renamed_in_mongo | pass | pass | halted-upstream | — | 0 | 101ms |
| create-then-rename-after-room-persists/stub | pass | pass | fail | — | 0 | 207ms |
| create-then-rename-after-room-persists[c=0 p=- x=-] | pass | pass | pass | — | 0 | 207ms |
| cross-site-message-federation/stub | fail | fail | fail | — | 0 | 15.1s |
| cross-site-room-rename-federation/stub | pass | pass | fail | — | 0 | 12.1s |
| cross-site-seed-visibility/stub | pass | pass | pass | — | 0 | 104ms |
| dm-create-idempotency-multi-input/stub | pass | pass | pass | — | 0 | 104ms |
| edit-after-persist-succeeds/edit | pass | pass | pass | — | 0 | 14ms |
| edit-after-persist-succeeds/edit_ok | pass | pass | pass | — | 0 | 100ms |
| edit-after-persist-succeeds/edited_canonical | pass | pass | pass | — | 0 | 101ms |
| edit-after-persist-succeeds/edited_persisted | pass | pass | pass | — | 0 | 3ms |
| edit-after-persist-succeeds/persisted | pass | pass | pass | — | 0 | 8ms |
| edit-after-persist-succeeds/send | pass | pass | pass | — | 0 | 0ms |
| edit-after-persist-succeeds/stub | pass | pass | pass | — | 0 | 226ms |
| edit-after-persist-succeeds[c=0 p=- x=-] | pass | pass | pass | — | 0 | 226ms |
| gatekeeper-create-room-then-send-races-subscription/stub | pass | pass | fail | — | 0 | 5.1s |
| gatekeeper-duplicate-message-id-cross-sender-deduped/stub | fail | pass | fail | — | 0 | 5.0s |
| gatekeeper-empty-content-rejected/stub | pass | pass | pass | — | 0 | 5.2s |
| gatekeeper-invalid-message-id-rejected/stub | pass | pass | pass | — | 0 | 5.2s |
| gatekeeper-large-room-bot-bypass/stub | pass | pass | pass | — | 0 | 102ms |
| gatekeeper-large-room-member-blocked/stub | pass | pass | pass | — | 0 | 5.2s |
| gatekeeper-large-room-owner-bypass/stub | pass | pass | fail | — | 0 | 104ms |
| gatekeeper-large-room-thread-reply-exempt/stub | pass | pass | pass | — | 0 | 102ms |
| gatekeeper-malformed-requestid-silent-drop/stub | pass | pass | fail | — | 0 | 10.1s |
| gatekeeper-not-subscribed-rejected/stub | pass | pass | pass | — | 0 | 5.2s |
| gatekeeper-quote-cross-context-mismatch-rejected/stub | pass | pass | pass | — | 0 | 5.2s |
| gatekeeper-quote-cross-room-drops-message/stub | pass | pass | pass | — | 0 | 5.1s |
| gatekeeper-quote-happy-path-embeds-snapshot/stub | pass | pass | pass | — | 0 | 104ms |
| gatekeeper-quote-just-sent-message/stub | pass | pass | fail | — | 0 | 5.2s |
| gatekeeper-quote-nonexistent-parent-drops-message/stub | pass | pass | pass | — | 0 | 10.1s |
| gatekeeper-quote-soft-deleted-message-resurrects-content/stub | pass | pass | pass | — | 0 | 140ms |
| gatekeeper-thread-reply-missing-parent-createdat-rejected/stub | pass | pass | pass | — | 0 | 5.2s |
| gatekeeper-whitespace-only-content-accepted/stub | pass | pass | pass | — | 0 | 105ms |
| history-service-get-message-by-id/stub | pass | pass | pass | — | 0 | 103ms |
| infra-sanity-rooms-pipeline-site-a/stub | pass | pass | fail | — | 0 | 205ms |
| infra-sanity-rooms-pipeline-site-b/stub | pass | pass | fail | — | 0 | 205ms |
| logs-tail-positive-captures-request-failed/stub | pass | pass | pass | — | 0 | 122ms |
| logs-tail-regression-guard-not-must-fail-when-line-present/stub | fail | fail | fail | — | 0 | 111ms |
| message-edit-just-sent-races-persistence/stub | pass | pass | fail | — | 0 | 205ms |
| message-edit-to-whitespace-rejected/stub | pass | pass | pass | — | 0 | 104ms |
| message-pipeline-send-and-persist/stub | pass | pass | pass | — | 0 | 214ms |
| message-pipeline-send-and-persist[c=0 p=- x=-] | pass | pass | pass | — | 0 | 214ms |
| message-worker-at-all-mention-persisted/stub | pass | pass | pass | — | 0 | 104ms |
| message-worker-cross-room-thread-reply-pollutes-foreign-parent/stub | pass | pass | pass | — | 0 | 105ms |
| message-worker-mentions-persisted/stub | pass | pass | pass | — | 0 | 104ms |
| message-worker-subsequent-thread-reply-multi-input/stub | fail | fail | fail | — | 0 | 10.1s |
| message-worker-thread-mention-nonmember-auto-subscribes/stub | pass | pass | pass | — | 0 | 102ms |
| message-worker-thread-reply-publishes-tcount-badge/stub | pass | pass | fail | — | 0 | 202ms |
| message-worker-thread-reply-with-mention/stub | pass | pass | pass | — | 0 | 102ms |
| message-worker-two-repliers-merge-replyaccounts/stub | pass | pass | pass | — | 0 | 103ms |
| room-create-federates-cross-site/stub | fail | fail | fail | — | 0 | 5.0s |
| room-creates-federates-to-site-b/stub | fail | fail | fail | — | 0 | 5.0s |
| single-site-room-create-baseline/stub | pass | pass | pass | — | 0 | 103ms |
| thread-first-reply-happy-path/stub | pass | pass | fail | — | 0 | 106ms |
| thread-first-reply-remote-parent-federates-subscription/stub | pass | pass | fail | — | 0 | 294ms |
| thread-reply-to-nonexistent-parent-creates-orphan/stub | pass | pass | pass | — | 0 | 5.1s |
