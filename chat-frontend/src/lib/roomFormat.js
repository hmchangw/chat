export function roomPrefix(type) {
  return type === 'dm' ? '@ ' : '# '
}

export function roomFromSearchHit(hit) {
  return {
    id: hit.roomId,
    name: hit.roomName,
    type: hit.roomType,
    siteId: hit.siteId,
  }
}

export function searchRoomPrefix(roomType) {
  return roomType === 'c' ? '#' : '@'
}
