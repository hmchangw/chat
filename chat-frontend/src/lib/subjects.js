// NATS subject builders — mirrors Go pkg/subject/subject.go
// Keep in sync with the Go definitions when adding new subjects.

export function msgSend(account, roomId, siteId) {
  return `chat.user.${account}.room.${roomId}.${siteId}.msg.send`
}

export function msgHistory(account, roomId, siteId) {
  return `chat.user.${account}.request.room.${roomId}.${siteId}.msg.history`
}

export function roomEvent(roomId) {
  return `chat.room.${roomId}.event`
}

export function roomsList(account) {
  return `chat.user.${account}.request.rooms.list`
}

export function roomsGet(account, roomId) {
  return `chat.user.${account}.request.rooms.get.${roomId}`
}

export function roomsCreate(account) {
  return `chat.user.${account}.request.rooms.create`
}

export function subscriptionUpdate(account) {
  return `chat.user.${account}.event.subscription.update`
}

export function roomMetadataUpdate(account) {
  return `chat.user.${account}.event.room.metadata.update`
}
