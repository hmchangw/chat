// Regression net for the centralized-error-codes migration: the predicate
// MUST accept both reply shapes during the backend rollout window (plan
// Ch 19). The legacy shape is removed in a follow-up release once the new
// envelope is everywhere.

import { describe, it, expect } from 'vitest'
import {
  isDMExistsReply,
  STATUS_EXISTS,
  ERR_DM_ALREADY_EXISTS,
} from './constants'

describe('isDMExistsReply', () => {
  it('matches the new success-envelope shape {status:"exists", roomId}', () => {
    expect(isDMExistsReply({ status: STATUS_EXISTS, roomId: 'r1' })).toBe(true)
  })

  it('still matches the legacy error-envelope shape {error:"dm already exists", roomId}', () => {
    expect(isDMExistsReply({ error: ERR_DM_ALREADY_EXISTS, roomId: 'r1' })).toBe(true)
  })

  it('does NOT match a real failure (no roomId)', () => {
    expect(isDMExistsReply({ error: ERR_DM_ALREADY_EXISTS })).toBe(false)
    expect(isDMExistsReply({ status: STATUS_EXISTS })).toBe(false)
  })

  it('does NOT match an unrelated error string with a roomId', () => {
    expect(isDMExistsReply({ error: 'something else', roomId: 'r1' })).toBe(false)
  })

  it('does NOT match an unrelated success status with a roomId', () => {
    expect(isDMExistsReply({ status: 'accepted', roomId: 'r1' })).toBe(false)
  })

  it('handles null / undefined / non-object inputs gracefully', () => {
    expect(isDMExistsReply(null)).toBe(false)
    expect(isDMExistsReply(undefined)).toBe(false)
    expect(isDMExistsReply({})).toBe(false)
  })
})
