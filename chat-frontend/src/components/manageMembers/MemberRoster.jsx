import { useCallback, useEffect, useMemo, useState } from 'react'
import { useNats } from '../../context/NatsContext'
import { memberList, memberRemove, memberRoleUpdate } from '../../lib/subjects'
import { ROLE_OWNER, ROLE_MEMBER } from '../../lib/constants'
import { formatAsyncJobError } from '../../lib/asyncJob'

export default function MemberRoster({ room }) {
  const { user, request, requestWithAsyncResult } = useNats()
  const [members, setMembers] = useState([])
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState(null)
  const [busyKey, setBusyKey] = useState(null)
  const [actionError, setActionError] = useState(null)

  // Depend on the primitive identity fields, not the room object reference —
  // a parent re-render that hands us an equivalent-but-new room object
  // shouldn't fire a redundant member.list.
  const fetchMembers = useCallback(async () => {
    setError(null)
    setLoading(true)
    try {
      const resp = await request(memberList(user.account, room.id, room.siteId), { enrich: true })
      setMembers(resp.members ?? [])
    } catch (err) {
      setError(err.message)
    } finally {
      setLoading(false)
    }
  }, [request, user.account, room.id, room.siteId])

  useEffect(() => {
    fetchMembers()
  }, [fetchMembers])

  // Owner-status is derived from the just-fetched roster: find the row that
  // matches the current user's account and read isOwner. Memoised so the
  // per-row gating doesn't re-derive on every render.
  const isCurrentUserOwner = useMemo(() => {
    const self = members.find((m) => m.member?.type === 'individual' && m.member?.account === user.account)
    return !!self?.member?.isOwner
  }, [members, user.account])

  // Single ordered list: orgs first, individuals second. Within each group
  // the server's enriched-list order is preserved (today: arbitrary; if the
  // server later sorts by sectName / engName the UI inherits that for free).
  const ordered = useMemo(() => {
    const orgs = members.filter((m) => m.member?.type === 'org')
    const individuals = members.filter((m) => m.member?.type === 'individual')
    return [...orgs, ...individuals]
  }, [members])

  /**
   * Run a member.{remove|role-update} action through requestWithAsyncResult,
   * surface failures as a banner, and refetch the roster on success.
   */
  const runAction = useCallback(
    async (key, subject, payload) => {
      setBusyKey(key)
      setActionError(null)
      try {
        await requestWithAsyncResult(subject, payload)
        await fetchMembers()
        return true
      } catch (err) {
        setActionError(formatAsyncJobError(err))
        return false
      } finally {
        setBusyKey(null)
      }
    },
    [requestWithAsyncResult, fetchMembers]
  )

  /**
   * Leave the room (member.remove on self). Distinct from the generic
   * runAction path because (a) we don't refetch — we're no longer a member
   * and the call would fail or return an empty roster, and (b) the dialog
   * will dismiss on its own via ChatPage's selectedRoom/summaries effect
   * once subscription.update lands.
   */
  const handleLeave = useCallback(async () => {
    if (!window.confirm(`Leave "${room.name}"?`)) return
    setBusyKey(`leave:${user.account}`)
    setActionError(null)
    try {
      await requestWithAsyncResult(memberRemove(user.account, room.id, room.siteId), {
        roomId: room.id,
        account: user.account,
      })
    } catch (err) {
      setActionError(formatAsyncJobError(err))
    } finally {
      setBusyKey(null)
    }
  }, [room.id, room.siteId, room.name, user.account, requestWithAsyncResult])

  if (loading) return <div className="roster-loading">Loading members…</div>
  if (error) return <div className="dialog-error">{error}</div>

  return (
    <div className="member-roster">
      {actionError && <div className="dialog-error">{actionError}</div>}

      {ordered.length === 0 ? (
        <div className="roster-empty">No members.</div>
      ) : (
        <ul className="roster-list">
          {ordered.map((m) =>
            m.member?.type === 'org'
              ? renderOrgRow(m, { busyKey, isCurrentUserOwner, room, user, runAction })
              : renderIndividualRow(m, {
                  busyKey,
                  isCurrentUserOwner,
                  room,
                  user,
                  runAction,
                  handleLeave,
                })
          )}
        </ul>
      )}
    </div>
  )
}

function renderOrgRow(m, { busyKey, isCurrentUserOwner, room, user, runAction }) {
  const entry = m.member
  const orgId = entry.id
  const display = entry.sectName || orgId
  const removeKey = `removeOrg:${orgId}`
  return (
    <li key={m.id} className="roster-row">
      <div className="roster-row-info">
        <span className="roster-name">{display}</span>
        {typeof entry.memberCount === 'number' && (
          <span className="roster-secondary">{entry.memberCount} members</span>
        )}
      </div>
      <div className="roster-row-actions">
        {isCurrentUserOwner && (
          <button
            type="button"
            aria-label={`Remove ${orgId}`}
            disabled={busyKey === removeKey}
            onClick={() =>
              runAction(
                removeKey,
                memberRemove(user.account, room.id, room.siteId),
                { roomId: room.id, orgId }
              )
            }
          >
            Remove
          </button>
        )}
      </div>
    </li>
  )
}

function renderIndividualRow(m, { busyKey, isCurrentUserOwner, room, user, runAction, handleLeave }) {
  const entry = m.member
  const isOwner = !!entry.isOwner
  const isSelf = entry.account === user.account
  const primary = entry.engName || entry.account
  const secondary = entry.chineseName || ''
  const promoteKey = `promote:${entry.account}`
  const demoteKey = `demote:${entry.account}`
  const removeKey = `remove:${entry.account}`
  const leaveKey = `leave:${entry.account}`

  return (
    <li key={m.id} className="roster-row">
      <div className="roster-row-info">
        <span className="roster-name">{primary}</span>
        {secondary && <span className="roster-secondary">{secondary}</span>}
        {isOwner && <span className="roster-badge">owner</span>}
      </div>
      <div className="roster-row-actions">
        {isSelf ? (
          // Self row: only Leave. Remove/Promote/Demote on self would let
          // the user kick themselves or lock themselves out of admin
          // mid-flow — Leave is the one self-applicable action and it
          // confirms first.
          <button
            type="button"
            disabled={busyKey === leaveKey}
            onClick={handleLeave}
          >
            Leave
          </button>
        ) : isCurrentUserOwner ? (
          <>
            {isOwner ? (
              <button
                type="button"
                aria-label={`Demote ${entry.account}`}
                disabled={busyKey === demoteKey}
                onClick={() =>
                  runAction(
                    demoteKey,
                    memberRoleUpdate(user.account, room.id, room.siteId),
                    { roomId: room.id, account: entry.account, newRole: ROLE_MEMBER }
                  )
                }
              >
                Demote
              </button>
            ) : (
              <button
                type="button"
                aria-label={`Promote ${entry.account}`}
                disabled={busyKey === promoteKey}
                onClick={() =>
                  runAction(
                    promoteKey,
                    memberRoleUpdate(user.account, room.id, room.siteId),
                    { roomId: room.id, account: entry.account, newRole: ROLE_OWNER }
                  )
                }
              >
                Promote
              </button>
            )}
            <button
              type="button"
              aria-label={`Remove ${entry.account}`}
              disabled={busyKey === removeKey}
              onClick={() =>
                runAction(
                  removeKey,
                  memberRemove(user.account, room.id, room.siteId),
                  { roomId: room.id, account: entry.account }
                )
              }
            >
              Remove
            </button>
          </>
        ) : null /* non-owner viewing someone else's row: no controls */}
      </div>
    </li>
  )
}
