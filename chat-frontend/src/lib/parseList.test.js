import { describe, it, expect } from 'vitest'
import { parseList } from './parseList'

describe('parseList', () => {
  it('splits on commas and trims whitespace', () => {
    expect(parseList('alice, bob,charlie  ')).toEqual(['alice', 'bob', 'charlie'])
  })

  it('filters out empty entries', () => {
    expect(parseList('alice,, ,bob')).toEqual(['alice', 'bob'])
  })

  it('returns an empty array for an empty string', () => {
    expect(parseList('')).toEqual([])
  })

  it('returns an empty array for whitespace-only input', () => {
    expect(parseList('   ,  ,  ')).toEqual([])
  })
})
