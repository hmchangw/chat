// Public surface of the api/ layer. Each operation hides its transport
// (NATS request/reply, JetStream publish, two-phase async-job) so callers
// just say `addMembers(nats, args)` and don't need to know what subject
// it lands on. Subject builders live in `_transport/subjects.js` and are
// considered internal to this folder.

export { addMembers } from './addMembers'
export { createRoom } from './createRoom'
export { deleteMessage } from './deleteMessage'
export { editMessage } from './editMessage'
export { fetchMessageHistory } from './fetchMessageHistory'
export { fetchReadReceipt } from './fetchReadReceipt'
export { fetchSurroundingMessages } from './fetchSurroundingMessages'
export { fetchThreadMessages } from './fetchThreadMessages'
export { getRoom } from './getRoom'
export { leaveRoom } from './leaveRoom'
export { listOrgMembers } from './listOrgMembers'
export { listRoomMembers } from './listRoomMembers'
export { listRooms } from './listRooms'
export { removeMember } from './removeMember'
export { searchMessages } from './searchMessages'
export { searchRooms } from './searchRooms'
export { sendMessage } from './sendMessage'
export { subscribeToRoomEvents } from './subscribeToRoomEvents'
export { subscribeToRoomMetadataUpdates } from './subscribeToRoomMetadataUpdates'
export { subscribeToSubscriptionUpdates } from './subscribeToSubscriptionUpdates'
export { subscribeToUserRoomEvents } from './subscribeToUserRoomEvents'
export { updateMemberRole } from './updateMemberRole'
