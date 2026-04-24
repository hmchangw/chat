import { describe, it, expect } from 'vitest'
import {
  userRoomEvent,
  roomEvent,
  searchMessages,
  searchRooms,
} from './subjects'

describe('subjects', () => {
  it('userRoomEvent builds the per-user room event subject', () => {
    expect(userRoomEvent('alice')).toBe('chat.user.alice.event.room')
  })

  it('roomEvent still builds the per-room subject', () => {
    expect(roomEvent('r1')).toBe('chat.room.r1.event')
  })

  it('searchMessages builds the message search request subject', () => {
    expect(searchMessages('alice')).toBe(
      'chat.user.alice.request.search.messages',
    )
  })

  it('searchRooms builds the room search request subject', () => {
    expect(searchRooms('alice')).toBe('chat.user.alice.request.search.rooms')
  })
})
