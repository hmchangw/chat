# Integration tests — total   (latest merge: d225aa9f)

Run:        2026-06-14T11:35:48Z   (runID 94b5)
Duration:   1m49.428s
Total:      42 test cases

## Confusion matrix

                        true (pass)   false (fail)
  +ve (through)         20            5            
  -ve (error/warning)   7             10           

## Status breakdown

              pass    fail
APPROVED      2       0      
DRAFT         25      15     

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
| create-then-rename-after-room-persists[c=0 p=- x=-] | pass | pass | pass | — | 0 | 206ms |
| cross-site-message-federation/stub | fail | fail | fail | — | 0 | 15.1s |
| cross-site-room-rename-federation/stub | pass | pass | fail | — | 0 | 12.1s |
| cross-site-room-rename-federation[c=0 p=- x=-] | pass | pass | pass | — | 0 | 12.1s |
| cross-site-seed-visibility/stub | pass | pass | pass | — | 0 | 104ms |
| cross-site-seed-visibility[c=0 p=- x=-] | pass | pass | pass | — | 0 | 104ms |
| dm-create-idempotency-multi-input/stub | pass | pass | pass | — | 0 | 105ms |
| dm-create-idempotency-multi-input[c=0 p=- x=-] | pass | pass | pass | — | 0 | 105ms |
| edit-after-persist-succeeds/edit | pass | pass | pass | — | 0 | 10ms |
| edit-after-persist-succeeds/edit_ok | pass | pass | pass | — | 0 | 100ms |
| edit-after-persist-succeeds/edited_canonical | pass | pass | pass | — | 0 | 101ms |
| edit-after-persist-succeeds/edited_persisted | pass | pass | pass | — | 0 | 3ms |
| edit-after-persist-succeeds/persisted | pass | pass | pass | — | 0 | 106ms |
| edit-after-persist-succeeds/send | pass | pass | pass | — | 0 | 0ms |
| edit-after-persist-succeeds/stub | pass | pass | pass | — | 0 | 319ms |
| edit-after-persist-succeeds[c=0 p=- x=-] | pass | pass | pass | — | 0 | 319ms |
| gatekeeper-create-room-then-send-races-subscription/stub | fail | pass | fail | — | 0 | 5.0s |
| gatekeeper-create-room-then-send-races-subscription[c=0 p=- x=-] | fail | fail | fail | — | 0 | 5.0s |
| gatekeeper-duplicate-message-id-cross-sender-deduped/stub | fail | pass | fail | — | 0 | 5.1s |
| gatekeeper-duplicate-message-id-cross-sender-deduped[c=0 p=- x=-] | fail | fail | fail | — | 0 | 5.1s |
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
| gatekeeper-malformed-requestid-silent-drop/stub | fail | pass | fail | — | 0 | 5.0s |
| gatekeeper-malformed-requestid-silent-drop[c=0 p=- x=-] | fail | fail | fail | — | 0 | 5.0s |
| gatekeeper-not-subscribed-rejected/stub | pass | pass | pass | — | 0 | 5.2s |
| gatekeeper-not-subscribed-rejected[c=0 p=- x=-] | pass | pass | pass | — | 0 | 5.2s |
| gatekeeper-quote-cross-context-mismatch-rejected/stub | pass | pass | pass | — | 0 | 5.2s |
| gatekeeper-quote-cross-context-mismatch-rejected[c=0 p=- x=-] | pass | pass | pass | — | 0 | 5.2s |
| gatekeeper-quote-cross-room-drops-message/stub | fail | pass | fail | — | 0 | 5.0s |
| gatekeeper-quote-cross-room-drops-message[c=0 p=- x=-] | fail | fail | fail | — | 0 | 5.0s |
| gatekeeper-quote-happy-path-embeds-snapshot/stub | pass | pass | pass | — | 0 | 103ms |
| gatekeeper-quote-happy-path-embeds-snapshot[c=0 p=- x=-] | pass | pass | pass | — | 0 | 103ms |
| gatekeeper-quote-just-sent-message/stub | fail | pass | fail | — | 0 | 5.1s |
| gatekeeper-quote-just-sent-message[c=0 p=- x=-] | fail | fail | fail | — | 0 | 5.1s |
| gatekeeper-quote-nonexistent-parent-drops-message/stub | fail | pass | fail | — | 0 | 5.0s |
| gatekeeper-quote-nonexistent-parent-drops-message[c=0 p=- x=-] | fail | fail | fail | — | 0 | 5.0s |
| gatekeeper-quote-soft-deleted-message-resurrects-content/stub | fail | pass | fail | — | 0 | 36ms |
| gatekeeper-quote-soft-deleted-message-resurrects-content[c=0 p=- x=-] | fail | fail | fail | — | 0 | 36ms |
| gatekeeper-sender-impersonation-blocked/stub | fail | fail | fail | — | 0 | 102ms |
| gatekeeper-sender-impersonation-blocked[c=0 p=- x=-] | fail | fail | fail | — | 0 | 102ms |
| gatekeeper-thread-reply-missing-parent-createdat-rejected/stub | pass | pass | pass | — | 0 | 5.2s |
| gatekeeper-thread-reply-missing-parent-createdat-rejected[c=0 p=- x=-] | pass | pass | pass | — | 0 | 5.2s |
| gatekeeper-whitespace-only-content-accepted/stub | fail | pass | fail | — | 0 | 102ms |
| gatekeeper-whitespace-only-content-accepted[c=0 p=- x=-] | fail | fail | fail | — | 0 | 102ms |
| history-service-get-message-by-id/stub | pass | pass | pass | — | 0 | 103ms |
| history-service-get-message-by-id[c=0 p=- x=-] | pass | pass | pass | — | 0 | 103ms |
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
| member-removal-leaves-thread-subscription[c=0 p=- x=-] | fail | fail | fail | — | 0 | 5.3s |
| message-edit-just-sent-races-persistence/stub | fail | pass | fail | — | 0 | 5.0s |
| message-edit-just-sent-races-persistence[c=0 p=- x=-] | fail | fail | fail | — | 0 | 5.0s |
| message-edit-to-whitespace-rejected/stub | pass | pass | pass | — | 0 | 103ms |
| message-edit-to-whitespace-rejected[c=0 p=- x=-] | pass | pass | pass | — | 0 | 103ms |
| message-pipeline-send-and-persist/stub | pass | pass | pass | — | 0 | 213ms |
| message-pipeline-send-and-persist[c=0 p=- x=-] | pass | pass | pass | — | 0 | 213ms |
| message-worker-at-all-mention-persisted/stub | pass | pass | pass | — | 0 | 104ms |
| message-worker-at-all-mention-persisted[c=0 p=- x=-] | pass | pass | pass | — | 0 | 104ms |
| message-worker-cross-room-thread-reply-pollutes-foreign-parent/stub | fail | pass | fail | — | 0 | 104ms |
| message-worker-cross-room-thread-reply-pollutes-foreign-parent[c=0 p=- x=-] | fail | fail | fail | — | 0 | 104ms |
| message-worker-mentions-persisted/stub | pass | pass | pass | — | 0 | 104ms |
| message-worker-mentions-persisted[c=0 p=- x=-] | pass | pass | pass | — | 0 | 104ms |
| message-worker-subsequent-thread-reply-multi-input/stub | fail | fail | fail | — | 0 | 10.1s |
| message-worker-subsequent-thread-reply-multi-input[c=0 p=- x=-] | fail | fail | fail | — | 0 | 10.1s |
| message-worker-thread-mention-nonmember-auto-subscribes/stub | fail | pass | fail | — | 0 | 102ms |
| message-worker-thread-mention-nonmember-auto-subscribes[c=0 p=- x=-] | fail | fail | fail | — | 0 | 102ms |
| message-worker-thread-reply-publishes-tcount-badge/stub | pass | pass | fail | — | 0 | 202ms |
| message-worker-thread-reply-with-mention/stub | pass | pass | pass | — | 0 | 102ms |
| message-worker-thread-reply-with-mention[c=0 p=- x=-] | pass | pass | pass | — | 0 | 102ms |
| message-worker-two-repliers-merge-replyaccounts/stub | pass | pass | pass | — | 0 | 103ms |
| message-worker-two-repliers-merge-replyaccounts[c=0 p=- x=-] | pass | pass | pass | — | 0 | 103ms |
| room-create-federates-cross-site/stub | fail | fail | fail | — | 0 | 5.0s |
| room-creates-federates-to-site-b/stub | fail | fail | fail | — | 0 | 5.0s |
| single-site-room-create-baseline/stub | pass | pass | pass | — | 0 | 103ms |
| thread-first-reply-happy-path/stub | pass | pass | fail | — | 0 | 106ms |
| thread-first-reply-happy-path[c=0 p=- x=-] | pass | pass | pass | — | 0 | 106ms |
| thread-first-reply-remote-parent-federates-subscription/stub | pass | pass | fail | — | 0 | 296ms |
| thread-first-reply-remote-parent-federates-subscription[c=0 p=- x=-] | pass | pass | pass | — | 0 | 296ms |
| thread-mention-remote-nonmember-federates-subscription/stub | fail | pass | fail | — | 0 | 202ms |
| thread-mention-remote-nonmember-federates-subscription[c=0 p=- x=-] | fail | fail | fail | — | 0 | 202ms |
| thread-reply-to-nonexistent-parent-creates-orphan/stub | pass | pass | pass | — | 0 | 5.1s |
| thread-reply-to-nonexistent-parent-creates-orphan[c=0 p=- x=-] | pass | pass | pass | — | 0 | 5.1s |

## Failure Details

### gatekeeper-quote-soft-deleted-message-resurrects-content — fail

- file: `scenarios/drafts/delete/gatekeeper-quote-soft-deleted-message-resurrects-content.yaml`
- subset: `scenario`  kind: `negative`  duration: 36ms
- reason:

```
Failed after 0.006s.
MatchShape: expected no event to match shape map[message_id:m0deletedmsg00000001 msg:secret content alice will delete], but event #0 (location="cassandra_select") did with payload map[attachments:<nil> card:<nil> card_action:<nil> created_at:2026-06-14 10:36:01.503Z deleted:true edited_at:<nil> enc_meta:<nil> enc_payload:<nil> file:<nil> mentions:<nil> message_id:m0deletedmsg00000001 msg:secret content alice will delete pinned_at:<nil> pinned_by:<nil> quoted_parent_message:<nil> reactions:<nil> room_id:r-deltest sender:map[account:alice app_id:<nil> app_name:<nil> company_name:alice eng_name:alice id:u-alice is_bot:<nil>] site_id:site-a sys_msg_data:<nil> tcount:<nil> thread_parent_created_at:<nil> thread_parent_id:<nil> thread_room_id:<nil> tshow:<nil> type:<nil> updated_at:2026-06-14 11:36:01.696Z visible_to:<nil>]
```

### thread-mention-remote-nonmember-federates-subscription — fail

- file: `scenarios/drafts/federation/thread-mention-remote-nonmember-federates-subscription.yaml`
- subset: `scenario`  kind: `negative`  duration: 202ms
- reason:

```
Failed after 0.000s.
MatchShape: expected no event to match shape map[userAccount:remotebob], but event #0 (location="mongo_find") did with payload map[_id:019ec5ead5d175fca07b5b69b294da91 createdAt:1781436962228 hasMention:true lastSeenAt:<nil> parentMessageId:m0fedmentparent00001 roomId:r-fedment siteId:site-a threadRoomId:019ec5ead5b57c56a5a67fd0ec3846b8 updatedAt:1781436962228 userAccount:remotebob userId:u-remotebob]
```

### gatekeeper-create-room-then-send-races-subscription — fail

- file: `scenarios/drafts/gatekeeper-validation/gatekeeper-create-room-then-send-races-subscription.yaml`
- subset: `scenario`  kind: `positive`  duration: 5.0s
- reason:

```
Timed out after 5.001s.
MatchShape failed.
  expected:          {"body_json":{"message":{"id":"m0createsend00000001"}}}
  reply from system: {"subject":"chat.msg.canonical.site-a.created","body_json":{"event":"created","message":{"content":"A new room has been created","createdAt":"2026-06-14T11:36:02.486Z","id":"vFWHOIzuoWEnnIQJ6quq","roomId":"dj905yEBtBuPxnQBC","sysMsgData":"eyJuYW1lIjoiRW5naW5lZXJpbmciLCJ1c2VycyI6WyJyb29tbWF0ZSJdLCJvcmdzIjpudWxsLCJjaGFubmVscyI6bnVsbCwiYWRkZWRVc2Vyc0NvdW50IjoxfQ==","type":"room_created","userAccount":"alice","userId":"u-alice"},"siteId":"site-a","timestamp":1781436962486},"header":{"Nats-Msg-Id":["vFWHOIzuoWEnnIQJ6quq"],"X-Request-ID":["019ec5ea-d6b4-7389-81da-e2dde3d0c859"]},"sequence":7}
  mismatch reason:   matches_shape: field "body_json.message.id": got "vFWHOIzuoWEnnIQJ6quq" want "m0createsend00000001"
  events polled:     2
```

### gatekeeper-duplicate-message-id-cross-sender-deduped — fail

- file: `scenarios/drafts/gatekeeper-validation/gatekeeper-duplicate-message-id-cross-sender-deduped.yaml`
- subset: `scenario`  kind: `negative`  duration: 5.1s
- reason:

```
Timed out after 5.001s.
MatchShape failed.
  expected:          {"received":[{"body_json":{"code":"conflict"}}]}
  reply from system: {"subject":"chat.user.bob.response.01970a4f-8c2d-7c9a-abcd-e01234567921","received":[{"subject":"chat.user.bob.response.01970a4f-8c2d-7c9a-abcd-e01234567921","body_json":{"content":"bob's colliding message","createdAt":"2026-06-14T11:36:07.661836671Z","id":"m0collideid000000001","roomId":"r-collide","userAccount":"bob","userDisplayName":"bob","userId":"u-bob"},"header":{"X-Request-ID":["019ec5ea-eaec-7752-9d82-c5e67de37e1e"]}}]}
  mismatch reason:   matches_shape: field "received": expected element [0] not found at or after observed[0] (cursor advanced through 0/1 elements)
MISSING: this element does not appear anywhere in observed
closest candidate's diff:
Expected
    <string>: 
to match keys: {
."body_json":
	missing expected key code
}

  events polled:     1
```

### gatekeeper-malformed-requestid-silent-drop — fail

- file: `scenarios/drafts/gatekeeper-validation/gatekeeper-malformed-requestid-silent-drop.yaml`
- subset: `scenario`  kind: `negative`  duration: 5.0s
- reason:

```
Timed out after 5.001s.
MatchShape failed.
  expected:          {"received":[{"body_json":{"code":"bad_request"}}]}
  reply from system: {"subject":"chat.user.alice.response.\u003e","received":[]}
  mismatch reason:   matches_shape: field "received": expected element [0] not found at or after observed[0] (cursor advanced through 0/0 elements)
MISSING: this element does not appear anywhere in observed
  events polled:     1
```

### gatekeeper-whitespace-only-content-accepted — fail

- file: `scenarios/drafts/gatekeeper-validation/gatekeeper-whitespace-only-content-accepted.yaml`
- subset: `scenario`  kind: `negative`  duration: 102ms
- reason:

```
Failed after 0.101s.
MatchShape: expected no event to match shape map[body_json:map[event:created message:map[id:m3whitespaceonly0001]]], but event #0 (location="jetstream_consume") did with payload {chat.msg.canonical.site-a.created map[event:created message:map[content:    createdAt:2026-06-14T11:36:39.407648127Z id:m3whitespaceonly0001 roomId:r-general userAccount:alice userDisplayName:alice userId:u-alice] siteId:site-a timestamp:1.781436999407e+12]  map[Nats-Msg-Id:[m3whitespaceonly0001] X-Request-ID:[01970a4f-8c2d-7c9a-abcd-e01234567904]] 10}
```

### message-edit-just-sent-races-persistence — fail

- file: `scenarios/drafts/lifecycle/message-edit-just-sent-races-persistence.yaml`
- subset: `scenario`  kind: `negative`  duration: 5.0s
- reason:

```
Timed out after 5.001s.
MatchShape failed.
  expected:          {"body_json":{"messageId":"m0editsent0000000001"}}
  reply from system: {"body_json":{"code":"not_found","error":"message not found"},"latency_ms":2}
  mismatch reason:   matches_shape: field "body_json.messageId" missing
  events polled:     1
```

### message-worker-thread-mention-nonmember-auto-subscribes — fail

- file: `scenarios/drafts/mentions/message-worker-thread-mention-nonmember-auto-subscribes.yaml`
- subset: `scenario`  kind: `negative`  duration: 102ms
- reason:

```
Failed after 0.101s.
MatchShape: expected no event to match shape map[userAccount:bob], but event #0 (location="mongo_find") did with payload map[_id:019ec5eb9efb70bb9ecf63bbaf6c9e0b createdAt:1781437013735 hasMention:true lastSeenAt:<nil> parentMessageId:m0mthrparent00000001 roomId:r-mthread siteId:site-a threadRoomId:019ec5eb9ee77811875fcb677e72419c updatedAt:1781437013735 userAccount:bob userId:u-bob]
```

### message-worker-subsequent-thread-reply-multi-input — fail

- file: `scenarios/drafts/message-worker-subsequent-thread-reply-multi-input.yaml`
- subset: `scenario`  kind: `positive`  duration: 10.1s
- reason:

```
Timed out after 10.001s.
MatchShape failed.
  expected:          {"message_id":"m0subseqparent000001","tcount":2}
  reply from system: {"attachments":null,"card":null,"card_action":null,"created_at":"2025-06-01 00:00:00.000Z","deleted":null,"edited_at":null,"enc_meta":null,"enc_payload":null,"file":null,"mentions":null,"message_id":"m0subseqparent000001","msg":"parent message for the subsequent-reply test","pinned_at":null,"pinned_by":null,"quoted_parent_message":null,"reactions":null,"room_id":"r-general","sender":{"account":"threadauthor","app_id":null,"app_name":null,"company_name":"threadauthor","eng_name":"threadauthor","id":"u-threadauthor","is_bot":null},"site_id":"site-a","sys_msg_data":null,"tcount":1,"thread_parent_created_at":null,"thread_parent_id":null,"thread_room_id":"019ec5eba161755d999dd613fe1fec7c","tshow":null,"type":null,"updated_at":null,"visible_to":null}
  mismatch reason:   matches_shape: field "tcount": got 1 want 2
  events polled:     1
```

### gatekeeper-quote-cross-room-drops-message — fail

- file: `scenarios/drafts/quote/gatekeeper-quote-cross-room-drops-message.yaml`
- subset: `scenario`  kind: `positive`  duration: 5.0s
- reason:

```
Timed out after 5.001s.
MatchShape failed.
  expected:          {"body_json":{"message":{"id":"m0crossroomquote0001"}}}
  reply from system: (no events polled)
  events polled:     0
```

### gatekeeper-quote-just-sent-message — fail

- file: `scenarios/drafts/quote/gatekeeper-quote-just-sent-message.yaml`
- subset: `scenario`  kind: `positive`  duration: 5.1s
- reason:

```
Timed out after 5.001s.
MatchShape failed.
  expected:          {"body_json":{"message":{"id":"m1quotingmsg00000001"}}}
  reply from system: {"subject":"chat.msg.canonical.site-a.created","body_json":{"event":"created","message":{"content":"the original message","createdAt":"2026-06-14T11:37:15.45680118Z","id":"m0origmsg00000000001","roomId":"r-general","userAccount":"alice","userDisplayName":"alice","userId":"u-alice"},"siteId":"site-a","timestamp":1781437035456},"header":{"Nats-Msg-Id":["m0origmsg00000000001"],"X-Request-ID":["01970a4f-8c2d-7c9a-abcd-e01234567912"]},"sequence":29}
  mismatch reason:   matches_shape: field "body_json.message.id": got "m0origmsg00000000001" want "m1quotingmsg00000001"
  events polled:     1
```

### gatekeeper-quote-nonexistent-parent-drops-message — fail

- file: `scenarios/drafts/quote/gatekeeper-quote-nonexistent-parent-drops-message.yaml`
- subset: `scenario`  kind: `positive`  duration: 5.0s
- reason:

```
Timed out after 5.000s.
MatchShape failed.
  expected:          {"body_json":{"message":{"id":"m2quotemiss000drop1a"}}}
  reply from system: (no events polled)
  events polled:     0
```

### gatekeeper-sender-impersonation-blocked — fail

- file: `scenarios/drafts/security/gatekeeper-sender-impersonation-blocked.yaml`
- subset: `scenario`  kind: `negative`  duration: 102ms
- reason:

```
Failed after 0.101s.
MatchShape: expected no event to match shape map[body_json:map[message:map[id:m0impersonate0000001]]], but event #0 (location="jetstream_consume") did with payload {chat.msg.canonical.site-a.created map[event:created message:map[content:pretending to be bob createdAt:2026-06-14T11:37:25.852849858Z id:m0impersonate0000001 roomId:r-imp userAccount:bob_imp userDisplayName:bob_imp userId:u-bob_imp] siteId:site-a timestamp:1.781437045852e+12]  map[Nats-Msg-Id:[m0impersonate0000001] X-Request-ID:[01970a4f-8c2d-7c9a-abcd-e01234560aa1]] 30}
```

### member-removal-leaves-thread-subscription — fail

- file: `scenarios/drafts/threads/member-removal-leaves-thread-subscription.yaml`
- subset: `scenario`  kind: `negative`  duration: 5.3s
- reason:

```
step "thread_sub_removed" (observe): expected "thread_sub_removed" (mongo_find): Failed after 0.000s.
MatchShape: expected no event to match shape map[userAccount:bob], but event #0 (location="mongo_find") did with payload map[_id:019ec5ec1d8a7a46917ce0a8f186ca84 createdAt:1781437046138 hasMention:false lastSeenAt:<nil> parentMessageId:m0rmthrparent0000001 roomId:r-rmthread siteId:site-a threadRoomId:019ec5ec1d7b7627827603f653e58922 updatedAt:1781437046138 userAccount:bob userId:u-bob]
```

### message-worker-cross-room-thread-reply-pollutes-foreign-parent — fail

- file: `scenarios/drafts/threads/message-worker-cross-room-thread-reply-pollutes-foreign-parent.yaml`
- subset: `scenario`  kind: `negative`  duration: 104ms
- reason:

```
Failed after 0.103s.
MatchShape: expected no event to match shape map[message_id:m0otherroomthread001 tcount:1], but event #0 (location="cassandra_select") did with payload map[attachments:<nil> card:<nil> card_action:<nil> created_at:2025-06-01 00:00:00.000Z deleted:<nil> edited_at:<nil> enc_meta:<nil> enc_payload:<nil> file:<nil> mentions:<nil> message_id:m0otherroomthread001 msg:a message in a room alice is not in pinned_at:<nil> pinned_by:<nil> quoted_parent_message:<nil> reactions:<nil> room_id:r-other sender:map[account:someone app_id:<nil> app_name:<nil> company_name:someone eng_name:someone id:u-someone is_bot:<nil>] site_id:site-a sys_msg_data:<nil> tcount:1 thread_parent_created_at:<nil> thread_parent_id:<nil> thread_room_id:019ec5ec32df78deba202f3cf8a422b1 tshow:<nil> type:<nil> updated_at:<nil> visible_to:<nil>]
```

