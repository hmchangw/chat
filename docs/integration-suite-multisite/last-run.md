# Integration tests — total   (latest merge: d225aa9f)

Run:        2026-06-13T19:31:30Z   (runID 8088)
Duration:   1m25.113s
Total:      27 test cases

## Confusion matrix

                        true (pass)   false (fail)
  +ve (through)         18            1            
  -ve (error/warning)   8             0            

## Status breakdown

              pass    fail
APPROVED      2       0      
DRAFT         24      1      

## Cases

| case | latest | best | worst | reads | cascades | duration |
|------|--------|------|-------|-------|----------|----------|
| cross-site-message-federation/stub | fail | fail | fail | — | 0 | 15.1s |
| cross-site-room-rename-federation/stub | pass | pass | fail | — | 0 | 12.2s |
| cross-site-room-rename-federation[c=0 p=- x=-] | pass | pass | pass | — | 0 | 12.2s |
| cross-site-seed-visibility/stub | pass | pass | pass | — | 0 | 104ms |
| cross-site-seed-visibility[c=0 p=- x=-] | pass | pass | pass | — | 0 | 104ms |
| dm-create-idempotency-multi-input/stub | pass | pass | pass | — | 0 | 104ms |
| dm-create-idempotency-multi-input[c=0 p=- x=-] | pass | pass | pass | — | 0 | 104ms |
| gatekeeper-empty-content-rejected/stub | pass | pass | pass | — | 0 | 5.2s |
| gatekeeper-empty-content-rejected[c=0 p=- x=-] | pass | pass | pass | — | 0 | 5.2s |
| gatekeeper-invalid-message-id-rejected/stub | pass | pass | pass | — | 0 | 5.2s |
| gatekeeper-invalid-message-id-rejected[c=0 p=- x=-] | pass | pass | pass | — | 0 | 5.2s |
| gatekeeper-large-room-bot-bypass/stub | pass | pass | pass | — | 0 | 102ms |
| gatekeeper-large-room-bot-bypass[c=0 p=- x=-] | pass | pass | pass | — | 0 | 102ms |
| gatekeeper-large-room-member-blocked/stub | pass | pass | pass | — | 0 | 5.2s |
| gatekeeper-large-room-member-blocked[c=0 p=- x=-] | pass | pass | pass | — | 0 | 5.2s |
| gatekeeper-large-room-owner-bypass/stub | pass | pass | fail | — | 0 | 104ms |
| gatekeeper-large-room-owner-bypass[c=0 p=- x=-] | pass | pass | pass | — | 0 | 104ms |
| gatekeeper-large-room-thread-reply-exempt/stub | pass | pass | pass | — | 0 | 102ms |
| gatekeeper-large-room-thread-reply-exempt[c=0 p=- x=-] | pass | pass | pass | — | 0 | 102ms |
| gatekeeper-malformed-requestid-silent-drop/stub | pass | pass | fail | — | 0 | 10.1s |
| gatekeeper-malformed-requestid-silent-drop[c=0 p=- x=-] | pass | pass | pass | — | 0 | 10.1s |
| gatekeeper-not-subscribed-rejected/stub | pass | pass | pass | — | 0 | 5.2s |
| gatekeeper-not-subscribed-rejected[c=0 p=- x=-] | pass | pass | pass | — | 0 | 5.2s |
| gatekeeper-quote-cross-context-mismatch-rejected/stub | pass | pass | pass | — | 0 | 5.2s |
| gatekeeper-quote-cross-context-mismatch-rejected[c=0 p=- x=-] | pass | pass | pass | — | 0 | 5.2s |
| gatekeeper-quote-happy-path-embeds-snapshot/stub | pass | pass | pass | — | 0 | 103ms |
| gatekeeper-quote-happy-path-embeds-snapshot[c=0 p=- x=-] | pass | pass | pass | — | 0 | 103ms |
| gatekeeper-quote-nonexistent-parent-drops-message/stub | pass | pass | pass | — | 0 | 10.1s |
| gatekeeper-quote-nonexistent-parent-drops-message[c=0 p=- x=-] | pass | pass | pass | — | 0 | 10.1s |
| gatekeeper-thread-reply-missing-parent-createdat-rejected/stub | pass | pass | pass | — | 0 | 5.2s |
| gatekeeper-thread-reply-missing-parent-createdat-rejected[c=0 p=- x=-] | pass | pass | pass | — | 0 | 5.2s |
| gatekeeper-whitespace-only-content-accepted/stub | pass | pass | pass | — | 0 | 107ms |
| gatekeeper-whitespace-only-content-accepted[c=0 p=- x=-] | pass | pass | pass | — | 0 | 107ms |
| history-service-get-message-by-id/stub | pass | pass | pass | — | 0 | 106ms |
| history-service-get-message-by-id[c=0 p=- x=-] | pass | pass | pass | — | 0 | 106ms |
| infra-sanity-rooms-pipeline-site-a/stub | pass | pass | fail | — | 0 | 204ms |
| infra-sanity-rooms-pipeline-site-a[c=0 p=- x=-] | pass | pass | pass | — | 0 | 204ms |
| infra-sanity-rooms-pipeline-site-b/stub | pass | pass | fail | — | 0 | 206ms |
| infra-sanity-rooms-pipeline-site-b[c=0 p=- x=-] | pass | pass | pass | — | 0 | 206ms |
| logs-tail-positive-captures-request-failed/stub | pass | pass | pass | — | 0 | 122ms |
| logs-tail-regression-guard-not-must-fail-when-line-present/stub | fail | fail | fail | — | 0 | 111ms |
| message-pipeline-send-and-persist/stub | pass | pass | pass | — | 0 | 212ms |
| message-pipeline-send-and-persist[c=0 p=- x=-] | pass | pass | pass | — | 0 | 212ms |
| message-worker-at-all-mention-persisted/stub | pass | pass | pass | — | 0 | 105ms |
| message-worker-at-all-mention-persisted[c=0 p=- x=-] | pass | pass | pass | — | 0 | 105ms |
| message-worker-mentions-persisted/stub | pass | pass | pass | — | 0 | 104ms |
| message-worker-mentions-persisted[c=0 p=- x=-] | pass | pass | pass | — | 0 | 104ms |
| message-worker-subsequent-thread-reply-multi-input/stub | fail | fail | fail | — | 0 | 10.1s |
| message-worker-subsequent-thread-reply-multi-input[c=0 p=- x=-] | fail | fail | fail | — | 0 | 10.1s |
| message-worker-thread-reply-publishes-tcount-badge/stub | pass | pass | fail | — | 0 | 202ms |
| message-worker-thread-reply-with-mention/stub | pass | pass | pass | — | 0 | 102ms |
| message-worker-thread-reply-with-mention[c=0 p=- x=-] | pass | pass | pass | — | 0 | 102ms |
| room-create-federates-cross-site/stub | fail | fail | fail | — | 0 | 5.0s |
| room-creates-federates-to-site-b/stub | fail | fail | fail | — | 0 | 5.0s |
| single-site-room-create-baseline/stub | pass | pass | pass | — | 0 | 103ms |
| thread-first-reply-happy-path/stub | pass | pass | fail | — | 0 | 106ms |
| thread-first-reply-happy-path[c=0 p=- x=-] | pass | pass | pass | — | 0 | 106ms |
| thread-first-reply-remote-parent-federates-subscription/stub | pass | pass | fail | — | 0 | 293ms |
| thread-first-reply-remote-parent-federates-subscription[c=0 p=- x=-] | pass | pass | pass | — | 0 | 293ms |
| thread-reply-to-nonexistent-parent-creates-orphan/stub | pass | pass | pass | — | 0 | 5.1s |
| thread-reply-to-nonexistent-parent-creates-orphan[c=0 p=- x=-] | pass | pass | pass | — | 0 | 5.1s |

## Failure Details

### message-worker-subsequent-thread-reply-multi-input — fail

- file: `scenarios/drafts/message-worker-subsequent-thread-reply-multi-input.yaml`
- subset: `scenario`  kind: `positive`  duration: 10.1s
- reason:

```
Timed out after 10.000s.
MatchShape failed.
  expected:          {"message_id":"m0subseqparent000001","tcount":2}
  reply from system: {"attachments":null,"card":null,"card_action":null,"created_at":"2025-06-01 00:00:00.000Z","deleted":null,"edited_at":null,"enc_meta":null,"enc_payload":null,"file":null,"mentions":null,"message_id":"m0subseqparent000001","msg":"parent message for the subsequent-reply test","pinned_at":null,"pinned_by":null,"quoted_parent_message":null,"reactions":null,"room_id":"r-general","sender":{"account":"threadauthor","app_id":null,"app_name":null,"company_name":"threadauthor","eng_name":"threadauthor","id":"u-threadauthor","is_bot":null},"site_id":"site-a","sys_msg_data":null,"tcount":1,"thread_parent_created_at":null,"thread_parent_id":null,"thread_room_id":"019ec278950376029f6ac437ca9b0813","tshow":null,"type":null,"updated_at":null,"visible_to":null}
  mismatch reason:   matches_shape: field "tcount": got 1 want 2
  events polled:     1
```

