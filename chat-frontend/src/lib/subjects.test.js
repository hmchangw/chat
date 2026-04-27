import { describe, it, expect } from 'vitest'
import { userRoomEvent, roomEvent, memberAdd, memberRemove, memberRoleUpdate } from './subjects'

describe('subjects', () => {
  it('userRoomEvent builds the per-user room event subject', () => {
    expect(userRoomEvent('alice')).toBe('chat.user.alice.event.room')
  })

  it('roomEvent still builds the per-room subject', () => {
    expect(roomEvent('r1')).toBe('chat.room.r1.event')
  })

  it('memberAdd builds the add-member request subject', () => {
    expect(memberAdd('alice', 'r1', 'site-A')).toBe(
      'chat.user.alice.request.room.r1.site-A.member.add'
    )
  })

  it('memberRemove builds the remove-member request subject', () => {
    expect(memberRemove('alice', 'r1', 'site-A')).toBe(
      'chat.user.alice.request.room.r1.site-A.member.remove'
    )
  })

  it('memberRoleUpdate builds the role-update request subject', () => {
    expect(memberRoleUpdate('alice', 'r1', 'site-A')).toBe(
      'chat.user.alice.request.room.r1.site-A.member.role-update'
    )
  })
})
