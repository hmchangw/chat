# New Step Phrasings — History Service

These step phrasings appear in
`tools/integration-suite/features/service/history-service.feature`
but are not present in the current registered vocabulary
(verified via `make -C tools/integration-suite steps`).

Each entry shows the Gherkin text used in the feature file, the
recommended regex to register it in a new or extended `*_steps_test.go`
file (suggested: `history_steps_test.go`), and the concrete NATS subject
and payload shape the step should construct.

---

## 1. Load History

**Gherkin:** `"<actor>" requests history for room "<roomName>"`

**Regex:**
```
^"([^"]+)" requests history for room "([^"]+)"$
```

**Implementation notes:**
- NATS subject: `subject.MsgHistoryPattern(siteID)` →
  `chat.user.<account>.request.room.<roomID>.<siteID>.msg.history`
- Payload: `models.LoadHistoryRequest{}` (all fields zero/omitted — minimal valid request)
- JSON wire shape (history-service/internal/models/message.go):
  ```json
  {}
  ```
- Store response via `natsRequest(ctx, suiteWorld, suiteConfig, actor, site, subj, body)`.

---

## 2. Load Next Messages (no cursor)

**Gherkin:** `"<actor>" requests next messages in room "<roomName>"`

**Regex:**
```
^"([^"]+)" requests next messages in room "([^"]+)"$
```

**Implementation notes:**
- NATS subject: `subject.MsgNextPattern(siteID)` →
  `chat.user.<account>.request.room.<roomID>.<siteID>.msg.next`
- Payload: `models.LoadNextMessagesRequest{}` (empty — no cursor, no after, no limit)
- JSON wire shape:
  ```json
  {}
  ```

---

## 3. Load Next Messages (with cursor)

**Gherkin:** `"<actor>" requests next messages in room "<roomName>" with cursor "<cursor>"`

**Regex:**
```
^"([^"]+)" requests next messages in room "([^"]+)" with cursor "([^"]+)"$
```

**Implementation notes:**
- Same subject as above.
- Payload: `models.LoadNextMessagesRequest{Cursor: "<cursor>"}`:
  ```json
  {"cursor": "<cursor>"}
  ```

---

## 4. Load Surrounding Messages (no message id)

**Gherkin:** `"<actor>" requests surrounding messages with no message id in room "<roomName>"`

**Regex:**
```
^"([^"]+)" requests surrounding messages with no message id in room "([^"]+)"$
```

**Implementation notes:**
- NATS subject: `subject.MsgSurroundingPattern(siteID)` →
  `chat.user.<account>.request.room.<roomID>.<siteID>.msg.surrounding`
- Payload: `models.LoadSurroundingMessagesRequest{MessageID: ""}`:
  ```json
  {"messageId": ""}
  ```

---

## 5. Load Surrounding Messages (unknown message id)

**Gherkin:** `"<actor>" requests surrounding messages for unknown message in room "<roomName>"`

**Regex:**
```
^"([^"]+)" requests surrounding messages for unknown message in room "([^"]+)"$
```

**Implementation notes:**
- Same subject as above.
- Use `idgen.GenerateMessageID()` for a well-formed but never-persisted ID (mirrors
  the `requestsThreadUnknownMessage` pattern already in `history_steps_test.go`).
- Payload: `models.LoadSurroundingMessagesRequest{MessageID: <generated>}`.

---

## 6. Get Message By ID (empty id)

**Gherkin:** `"<actor>" requests message by id "" in room "<roomName>"`

**Regex:**
```
^"([^"]+)" requests message by id "" in room "([^"]+)"$
```

**Implementation notes:**
- NATS subject: `subject.MsgGetPattern(siteID)` →
  `chat.user.<account>.request.room.<roomID>.<siteID>.msg.get`
- Payload: `models.GetMessageByIDRequest{MessageID: ""}`:
  ```json
  {"messageId": ""}
  ```

---

## 7. Get Message By ID (unknown id)

**Gherkin:** `"<actor>" requests message by unknown id in room "<roomName>"`

**Regex:**
```
^"([^"]+)" requests message by unknown id in room "([^"]+)"$
```

**Implementation notes:**
- Same subject as above.
- Use `idgen.GenerateMessageID()` for a well-formed but never-persisted ID.
- Payload: `models.GetMessageByIDRequest{MessageID: <generated>}`.

---

## 8. Edit Message (by message id)

**Gherkin:** `"<actor>" edits message "<messageID>" in room "<roomName>" with new content "<content>"`

**Regex:**
```
^"([^"]+)" edits message "([^"]*)" in room "([^"]+)" with new content "([^"]+)"$
```

**Implementation notes:**
- NATS subject: `subject.MsgEditPattern(siteID)` →
  `chat.user.<account>.request.room.<roomID>.<siteID>.msg.edit`
- Payload (history-service/internal/models/message.go `EditMessageRequest`):
  ```json
  {"messageId": "<messageID>", "newMsg": "<content>"}
  ```

---

## 9. Edit Message with empty content (unknown message)

**Gherkin:** `"<actor>" edits unknown message in room "<roomName>" with empty content`

**Regex:**
```
^"([^"]+)" edits unknown message in room "([^"]+)" with empty content$
```

**Implementation notes:**
- Same subject as above.
- Use `idgen.GenerateMessageID()` for the messageId.
- Payload: `EditMessageRequest{MessageID: <generated>, NewMsg: ""}`:
  ```json
  {"messageId": "<generated>", "newMsg": ""}
  ```

---

## 10. Delete Message (by message id, empty string)

**Gherkin:** `"<actor>" deletes message "" in room "<roomName>"`

**Regex:**
```
^"([^"]+)" deletes message "" in room "([^"]+)"$
```

**Implementation notes:**
- NATS subject: `subject.MsgDeletePattern(siteID)` →
  `chat.user.<account>.request.room.<roomID>.<siteID>.msg.delete`
- Payload (history-service/internal/models/message.go `DeleteMessageRequest`):
  ```json
  {"messageId": ""}
  ```

---

## 11. Delete Message (unknown id)

**Gherkin:** `"<actor>" deletes unknown message in room "<roomName>"`

**Regex:**
```
^"([^"]+)" deletes unknown message in room "([^"]+)"$
```

**Implementation notes:**
- Same subject as above.
- Use `idgen.GenerateMessageID()` for a well-formed but never-persisted ID.
- Payload: `DeleteMessageRequest{MessageID: <generated>}`.

---

## 12. List Thread Parent Messages (default filter)

**Gherkin:** `"<actor>" requests thread parent messages in room "<roomName>"`

**Regex:**
```
^"([^"]+)" requests thread parent messages in room "([^"]+)"$
```

**Implementation notes:**
- NATS subject: `subject.MsgThreadParentPattern(siteID)` →
  `chat.user.<account>.request.room.<roomID>.<siteID}.msg.thread.parent`
- Payload (history-service/internal/models/thread_parent.go `GetThreadParentMessagesRequest`):
  ```json
  {}
  ```
  (empty filter defaults to "all" per `validateThreadFilter`)

---

## 13. List Thread Parent Messages (specified filter)

**Gherkin:** `"<actor>" requests thread parent messages in room "<roomName>" with filter "<filter>"`

**Regex:**
```
^"([^"]+)" requests thread parent messages in room "([^"]+)" with filter "([^"]+)"$
```

**Implementation notes:**
- Same subject as above.
- Payload: `GetThreadParentMessagesRequest{Filter: "<filter>"}`:
  ```json
  {"filter": "<filter>"}
  ```

---

## 14. Get Thread Messages with cursor

**Gherkin:** `"<actor>" requests thread messages with cursor "<cursor>" for thread "<threadID>" in room "<roomName>"`

**Regex:**
```
^"([^"]+)" requests thread messages with cursor "([^"]+)" for thread "([^"]+)" in room "([^"]+)"$
```

**Implementation notes:**
- NATS subject: `subject.MsgThread(account, roomID, siteID)` →
  `chat.user.<account>.request.room.<roomID>.<siteID>.msg.thread`
- Payload (history-service/internal/models/message.go `GetThreadMessagesRequest`):
  ```json
  {"threadMessageId": "<threadID>", "cursor": "<cursor>"}
  ```
- The `threadMessagesRequest` struct is already defined in `history_steps_test.go`;
  add a `cursor` field to the existing struct and write this as a new step in that file.

---

## Summary

| # | Gherkin text (paraphrased) | Regex key tokens | New file (suggested) |
|---|---------------------------|------------------|----------------------|
| 1 | requests history for room | `requests history for room` | history_steps_test.go |
| 2 | requests next messages in room (no cursor) | `requests next messages in room` | history_steps_test.go |
| 3 | requests next messages in room with cursor | `with cursor` | history_steps_test.go |
| 4 | surrounding messages, no message id | `surrounding messages with no message id` | history_steps_test.go |
| 5 | surrounding messages, unknown id | `surrounding messages for unknown message` | history_steps_test.go |
| 6 | get message by id "" | `requests message by id ""` | history_steps_test.go |
| 7 | get message by unknown id | `requests message by unknown id` | history_steps_test.go |
| 8 | edits message "<id>" with content | `edits message` | history_steps_test.go |
| 9 | edits unknown message with empty content | `edits unknown message` | history_steps_test.go |
| 10 | deletes message "" | `deletes message ""` | history_steps_test.go |
| 11 | deletes unknown message | `deletes unknown message` | history_steps_test.go |
| 12 | thread parent messages (no filter) | `requests thread parent messages in room` | history_steps_test.go |
| 13 | thread parent messages with filter | `with filter` | history_steps_test.go |
| 14 | thread messages with cursor | `requests thread messages with cursor` | history_steps_test.go |

**Total new steps: 14**
