# Integration tests — approved   (latest merge: d225aa9f)

Run:        2026-06-14T11:35:48Z   (runID 94b5)
Duration:   1m49.428s
Total:      2 test cases

## Confusion matrix

                        true (pass)   false (fail)
  +ve (through)         2             0            
  -ve (error/warning)   0             0            

## Status breakdown

              pass    fail
APPROVED      2       0      
DRAFT         0       0      

## Cases

| case | latest | best | worst | reads | cascades | duration |
|------|--------|------|-------|-------|----------|----------|
| create-then-rename-after-room-persists/create | pass | pass | pass | — | 0 | 2ms |
| create-then-rename-after-room-persists/create_accepted | pass | pass | fail | — | 0 | 100ms |
| create-then-rename-after-room-persists/rename | pass | pass | halted-upstream | — | 0 | 1ms |
| create-then-rename-after-room-persists/rename_accepted | pass | pass | halted-upstream | — | 0 | 0ms |
| create-then-rename-after-room-persists/rename_canonical | pass | pass | halted-upstream | — | 0 | 100ms |
| create-then-rename-after-room-persists/room_persisted | pass | pass | halted-upstream | — | 0 | 0ms |
| create-then-rename-after-room-persists/room_renamed_in_mongo | pass | pass | halted-upstream | — | 0 | 101ms |
| create-then-rename-after-room-persists/stub | pass | pass | fail | — | 0 | 206ms |
| cross-site-message-federation/stub | fail | fail | fail | — | 0 | 15.1s |
| cross-site-room-rename-federation/stub | pass | pass | fail | — | 0 | 12.1s |
| cross-site-seed-visibility/stub | pass | pass | pass | — | 0 | 104ms |
| dm-create-idempotency-multi-input/stub | pass | pass | pass | — | 0 | 105ms |
| edit-after-persist-succeeds/edit | pass | pass | pass | — | 0 | 10ms |
| edit-after-persist-succeeds/edit_ok | pass | pass | pass | — | 0 | 100ms |
| edit-after-persist-succeeds/edited_canonical | pass | pass | pass | — | 0 | 101ms |
| edit-after-persist-succeeds/edited_persisted | pass | pass | pass | — | 0 | 3ms |
| edit-after-persist-succeeds/persisted | pass | pass | pass | — | 0 | 106ms |
| edit-after-persist-succeeds/send | pass | pass | pass | — | 0 | 0ms |
| edit-after-persist-succeeds/stub | pass | pass | pass | — | 0 | 319ms |
| gatekeeper-create-room-then-send-races-subscription/stub | fail | pass | fail | — | 0 | 5.0s |
| gatekeeper-duplicate-message-id-cross-sender-deduped/stub | fail | pass | fail | — | 0 | 5.1s |
| gatekeeper-empty-content-rejected/stub | pass | pass | pass | — | 0 | 5.2s |
| gatekeeper-invalid-message-id-rejected/stub | pass | pass | pass | — | 0 | 5.2s |
| gatekeeper-large-room-bot-bypass/stub | pass | pass | pass | — | 0 | 102ms |
| gatekeeper-large-room-member-blocked/stub | pass | pass | pass | — | 0 | 5.2s |
| gatekeeper-large-room-owner-bypass/stub | pass | pass | fail | — | 0 | 104ms |
| gatekeeper-large-room-thread-reply-exempt/stub | pass | pass | pass | — | 0 | 102ms |
| gatekeeper-malformed-requestid-silent-drop/stub | fail | pass | fail | — | 0 | 5.0s |
| gatekeeper-not-subscribed-rejected/stub | pass | pass | pass | — | 0 | 5.2s |
| gatekeeper-quote-cross-context-mismatch-rejected/stub | pass | pass | pass | — | 0 | 5.2s |
| gatekeeper-quote-cross-room-drops-message/stub | fail | pass | fail | — | 0 | 5.0s |
| gatekeeper-quote-happy-path-embeds-snapshot/stub | pass | pass | pass | — | 0 | 103ms |
| gatekeeper-quote-just-sent-message/stub | fail | pass | fail | — | 0 | 5.1s |
| gatekeeper-quote-nonexistent-parent-drops-message/stub | fail | pass | fail | — | 0 | 5.0s |
| gatekeeper-quote-soft-deleted-message-resurrects-content/stub | fail | pass | fail | — | 0 | 36ms |
| gatekeeper-sender-impersonation-blocked/stub | fail | fail | fail | — | 0 | 102ms |
| gatekeeper-thread-reply-missing-parent-createdat-rejected/stub | pass | pass | pass | — | 0 | 5.2s |
| gatekeeper-whitespace-only-content-accepted/stub | fail | pass | fail | — | 0 | 102ms |
| history-service-get-message-by-id/stub | pass | pass | pass | — | 0 | 103ms |
| infra-sanity-rooms-pipeline-site-a/stub | pass | pass | fail | — | 0 | 205ms |
| infra-sanity-rooms-pipeline-site-a[c=0 p=- x=-] | pass | pass | pass | — | 0 | 205ms |
| infra-sanity-rooms-pipeline-site-b/stub | pass | pass | fail | — | 0 | 205ms |
| infra-sanity-rooms-pipeline-site-b[c=0 p=- x=-] | pass | pass | pass | — | 0 | 205ms |
| logs-tail-positive-captures-request-failed/stub | pass | pass | pass | — | 0 | 122ms |
| logs-tail-regression-guard-not-must-fail-when-line-present/stub | fail | fail | fail | — | 0 | 111ms |
| member-removal-leaves-thread-subscription/bob_reply | pass | pass | pass | — | 0 | 1ms |
| member-removal-leaves-thread-subscription/bob_thread_sub | pass | pass | pass | — | 0 | 101ms |
| member-removal-leaves-thread-subscription/remove | pass | pass | pass | — | 0 | 3ms |
| member-removal-leaves-thread-subscription/remove_accepted | pass | pass | pass | — | 0 | 100ms |
| member-removal-leaves-thread-subscription/remove_processed | pass | pass | pass | — | 0 | 101ms |
| member-removal-leaves-thread-subscription/room_sub_gone | pass | pass | pass | — | 0 | 5.0s |
| member-removal-leaves-thread-subscription/stub | fail | pass | fail | — | 0 | 5.3s |
| member-removal-leaves-thread-subscription/thread_sub_removed | fail | fail | fail | — | 0 | 0ms |
| member-removal-leaves-thread-subscription/thread_sub_survives | pass | pass | pass | — | 0 | 0ms |
| message-edit-just-sent-races-persistence/stub | fail | pass | fail | — | 0 | 5.0s |
| message-edit-to-whitespace-rejected/stub | pass | pass | pass | — | 0 | 103ms |
| message-pipeline-send-and-persist/stub | pass | pass | pass | — | 0 | 213ms |
| message-worker-at-all-mention-persisted/stub | pass | pass | pass | — | 0 | 104ms |
| message-worker-cross-room-thread-reply-pollutes-foreign-parent/stub | fail | pass | fail | — | 0 | 104ms |
| message-worker-mentions-persisted/stub | pass | pass | pass | — | 0 | 104ms |
| message-worker-subsequent-thread-reply-multi-input/stub | fail | fail | fail | — | 0 | 10.1s |
| message-worker-thread-mention-nonmember-auto-subscribes/stub | fail | pass | fail | — | 0 | 102ms |
| message-worker-thread-reply-publishes-tcount-badge/stub | pass | pass | fail | — | 0 | 202ms |
| message-worker-thread-reply-with-mention/stub | pass | pass | pass | — | 0 | 102ms |
| message-worker-two-repliers-merge-replyaccounts/stub | pass | pass | pass | — | 0 | 103ms |
| room-create-federates-cross-site/stub | fail | fail | fail | — | 0 | 5.0s |
| room-creates-federates-to-site-b/stub | fail | fail | fail | — | 0 | 5.0s |
| single-site-room-create-baseline/stub | pass | pass | pass | — | 0 | 103ms |
| thread-first-reply-happy-path/stub | pass | pass | fail | — | 0 | 106ms |
| thread-first-reply-remote-parent-federates-subscription/stub | pass | pass | fail | — | 0 | 296ms |
| thread-mention-remote-nonmember-federates-subscription/stub | fail | pass | fail | — | 0 | 202ms |
| thread-reply-to-nonexistent-parent-creates-orphan/stub | pass | pass | pass | — | 0 | 5.1s |
