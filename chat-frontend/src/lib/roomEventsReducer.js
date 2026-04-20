export const MAX_CACHED = 200

export const initialState = {
  summaries: [],
  roomState: {},
  activeRoomId: null,
  roomsError: null,
}

function sortByLastMsgDesc(summaries) {
  return [...summaries].sort((a, b) => {
    const at = a.lastMsgAt ? new Date(a.lastMsgAt).getTime() : 0
    const bt = b.lastMsgAt ? new Date(b.lastMsgAt).getTime() : 0
    return bt - at
  })
}

function toSummary(room) {
  return {
    id: room.id,
    name: room.name,
    type: room.type,
    siteId: room.siteId,
    userCount: room.userCount,
    lastMsgAt: room.lastMsgAt ?? null,
    unreadCount: 0,
    hasMention: false,
    mentionAll: false,
  }
}

export function roomEventsReducer(state, action) {
  switch (action.type) {
    case 'ROOMS_LOADED': {
      const summaries = sortByLastMsgDesc(action.rooms.map(toSummary))
      return { ...state, summaries, roomsError: null }
    }
    case 'ROOM_ADDED': {
      if (state.summaries.some((r) => r.id === action.room.id)) return state
      const summaries = sortByLastMsgDesc([...state.summaries, toSummary(action.room)])
      return { ...state, summaries }
    }
    case 'ROOM_REMOVED': {
      const summaries = state.summaries.filter((r) => r.id !== action.roomId)
      const { [action.roomId]: _removed, ...rest } = state.roomState
      return { ...state, summaries, roomState: rest }
    }
    case 'ROOM_METADATA_UPDATED': {
      if (!state.summaries.some((r) => r.id === action.roomId)) return state
      const summaries = sortByLastMsgDesc(
        state.summaries.map((r) =>
          r.id === action.roomId
            ? { ...r, name: action.name, userCount: action.userCount, lastMsgAt: action.lastMsgAt }
            : r
        )
      )
      return { ...state, summaries }
    }
    default:
      return state
  }
}
