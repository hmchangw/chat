import { describe, it, expect } from 'vitest'
import { initialState, roomEventsReducer } from './roomEventsReducer'

function room(id, overrides = {}) {
  return {
    id,
    name: `room-${id}`,
    type: 'group',
    siteId: 'site-A',
    userCount: 2,
    lastMsgAt: '2026-04-17T10:00:00Z',
    ...overrides,
  }
}

describe('roomEventsReducer: rooms actions', () => {
  it('ROOMS_LOADED populates summaries sorted by lastMsgAt desc', () => {
    const a = room('a', { lastMsgAt: '2026-04-17T10:00:00Z' })
    const b = room('b', { lastMsgAt: '2026-04-17T12:00:00Z' })
    const next = roomEventsReducer(initialState, {
      type: 'ROOMS_LOADED',
      rooms: [a, b],
    })
    expect(next.summaries.map((r) => r.id)).toEqual(['b', 'a'])
    expect(next.summaries[0]).toMatchObject({
      id: 'b',
      name: 'room-b',
      type: 'group',
      unreadCount: 0,
      hasMention: false,
      mentionAll: false,
    })
  })

  it('ROOM_ADDED appends a room and keeps sort order', () => {
    const a = room('a', { lastMsgAt: '2026-04-17T09:00:00Z' })
    const state = roomEventsReducer(initialState, { type: 'ROOMS_LOADED', rooms: [a] })
    const b = room('b', { lastMsgAt: '2026-04-17T10:00:00Z' })
    const next = roomEventsReducer(state, { type: 'ROOM_ADDED', room: b })
    expect(next.summaries.map((r) => r.id)).toEqual(['b', 'a'])
  })

  it('ROOM_ADDED ignores duplicates', () => {
    const a = room('a')
    const state = roomEventsReducer(initialState, { type: 'ROOMS_LOADED', rooms: [a] })
    const next = roomEventsReducer(state, { type: 'ROOM_ADDED', room: a })
    expect(next.summaries).toHaveLength(1)
  })

  it('ROOM_REMOVED drops the room from summaries and clears roomState', () => {
    const a = room('a')
    const b = room('b')
    const state = roomEventsReducer(initialState, { type: 'ROOMS_LOADED', rooms: [a, b] })
    const withCache = {
      ...state,
      roomState: {
        a: { messages: [], hasLoadedHistory: false, historyError: null, unreadCount: 1, hasMention: false, mentionAll: false, lastMsgAt: null, lastMsgId: null },
      },
    }
    const next = roomEventsReducer(withCache, { type: 'ROOM_REMOVED', roomId: 'a' })
    expect(next.summaries.map((r) => r.id)).toEqual(['b'])
    expect(next.roomState.a).toBeUndefined()
  })

  it('ROOM_METADATA_UPDATED patches name/userCount/lastMsgAt and re-sorts', () => {
    const a = room('a', { lastMsgAt: '2026-04-17T09:00:00Z' })
    const b = room('b', { lastMsgAt: '2026-04-17T10:00:00Z' })
    const state = roomEventsReducer(initialState, { type: 'ROOMS_LOADED', rooms: [a, b] })
    const next = roomEventsReducer(state, {
      type: 'ROOM_METADATA_UPDATED',
      roomId: 'a',
      name: 'a-renamed',
      userCount: 5,
      lastMsgAt: '2026-04-17T11:00:00Z',
    })
    expect(next.summaries[0]).toMatchObject({ id: 'a', name: 'a-renamed', userCount: 5 })
  })

  it('ROOM_METADATA_UPDATED for unknown room is a no-op', () => {
    const next = roomEventsReducer(initialState, {
      type: 'ROOM_METADATA_UPDATED',
      roomId: 'missing',
      name: 'x',
      userCount: 1,
      lastMsgAt: '2026-04-17T11:00:00Z',
    })
    expect(next).toBe(initialState)
  })
})

function newMessageEvent(overrides = {}) {
  return {
    type: 'new_message',
    roomId: 'a',
    roomName: 'room-a',
    roomType: 'group',
    siteId: 'site-A',
    userCount: 3,
    lastMsgAt: '2026-04-17T12:00:00Z',
    lastMsgId: 'm1',
    mentionAll: false,
    hasMention: false,
    message: {
      id: 'm1',
      roomId: 'a',
      content: 'hi',
      createdAt: '2026-04-17T12:00:00Z',
      sender: { account: 'bob', engName: 'Bob' },
    },
    timestamp: 1,
    ...overrides,
  }
}

describe('roomEventsReducer: MESSAGE_RECEIVED', () => {
  it('appends a message and seeds roomState for an unknown room', () => {
    const next = roomEventsReducer(initialState, {
      type: 'MESSAGE_RECEIVED',
      event: newMessageEvent(),
    })
    expect(next.roomState.a.messages).toHaveLength(1)
    expect(next.roomState.a.messages[0].id).toBe('m1')
    expect(next.roomState.a.unreadCount).toBe(1)
    expect(next.roomState.a.lastMsgAt).toBe('2026-04-17T12:00:00Z')
    expect(next.roomState.a.lastMsgId).toBe('m1')
  })

  it('deduplicates by message.id', () => {
    const s1 = roomEventsReducer(initialState, {
      type: 'MESSAGE_RECEIVED',
      event: newMessageEvent(),
    })
    const s2 = roomEventsReducer(s1, {
      type: 'MESSAGE_RECEIVED',
      event: newMessageEvent(),
    })
    expect(s2.roomState.a.messages).toHaveLength(1)
    expect(s2.roomState.a.unreadCount).toBe(1)
  })

  it('does not increment unreadCount for the active room', () => {
    const state = { ...initialState, activeRoomId: 'a' }
    const next = roomEventsReducer(state, {
      type: 'MESSAGE_RECEIVED',
      event: newMessageEvent(),
    })
    expect(next.roomState.a.unreadCount).toBe(0)
  })

  it('sets hasMention when event.hasMention is true and room is not active', () => {
    const next = roomEventsReducer(initialState, {
      type: 'MESSAGE_RECEIVED',
      event: newMessageEvent({ hasMention: true }),
    })
    expect(next.roomState.a.hasMention).toBe(true)
    expect(next.roomState.a.mentionAll).toBe(false)
  })

  it('sets mentionAll when event.mentionAll is true and room is not active', () => {
    const next = roomEventsReducer(initialState, {
      type: 'MESSAGE_RECEIVED',
      event: newMessageEvent({ mentionAll: true }),
    })
    expect(next.roomState.a.mentionAll).toBe(true)
  })

  it('does not set mention flags for the active room', () => {
    const state = { ...initialState, activeRoomId: 'a' }
    const next = roomEventsReducer(state, {
      type: 'MESSAGE_RECEIVED',
      event: newMessageEvent({ hasMention: true, mentionAll: true }),
    })
    expect(next.roomState.a.hasMention).toBe(false)
    expect(next.roomState.a.mentionAll).toBe(false)
  })

  it('updates matching summary lastMsgAt and resorts', () => {
    const a = { id: 'a', name: 'a', type: 'group', siteId: 'site-A', userCount: 2, lastMsgAt: '2026-04-17T08:00:00Z' }
    const b = { id: 'b', name: 'b', type: 'group', siteId: 'site-A', userCount: 2, lastMsgAt: '2026-04-17T09:00:00Z' }
    const loaded = roomEventsReducer(initialState, { type: 'ROOMS_LOADED', rooms: [a, b] })
    const next = roomEventsReducer(loaded, {
      type: 'MESSAGE_RECEIVED',
      event: newMessageEvent({ roomId: 'a', lastMsgAt: '2026-04-17T10:00:00Z' }),
    })
    expect(next.summaries.map((r) => r.id)).toEqual(['a', 'b'])
    expect(next.summaries[0].lastMsgAt).toBe('2026-04-17T10:00:00Z')
    expect(next.summaries[0].unreadCount).toBe(1)
  })

  it('caps the cached messages at MAX_CACHED, dropping oldest', async () => {
    const { MAX_CACHED } = await import('./roomEventsReducer')
    let state = initialState
    for (let i = 0; i < MAX_CACHED + 5; i++) {
      state = roomEventsReducer(state, {
        type: 'MESSAGE_RECEIVED',
        event: newMessageEvent({
          message: {
            id: `m${i}`,
            roomId: 'a',
            content: String(i),
            createdAt: '2026-04-17T12:00:00Z',
            sender: { account: 'bob', engName: 'Bob' },
          },
        }),
      })
    }
    const msgs = state.roomState.a.messages
    expect(msgs).toHaveLength(MAX_CACHED)
    expect(msgs[0].id).toBe('m5')
    expect(msgs[MAX_CACHED - 1].id).toBe(`m${MAX_CACHED + 4}`)
  })
})
