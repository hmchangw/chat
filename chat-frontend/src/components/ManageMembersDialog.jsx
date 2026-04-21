import { useState } from 'react'
import AddMembersForm from './manageMembers/AddMembersForm'
import RemoveMemberForm from './manageMembers/RemoveMemberForm'
import RoleUpdateForm from './manageMembers/RoleUpdateForm'
import RemoveOrgForm from './manageMembers/RemoveOrgForm'

const TABS = [
  { id: 'add', label: 'Add', Form: AddMembersForm },
  { id: 'remove', label: 'Remove', Form: RemoveMemberForm },
  { id: 'role', label: 'Role', Form: RoleUpdateForm },
  { id: 'removeOrg', label: 'Remove Org', Form: RemoveOrgForm },
]

export default function ManageMembersDialog({ room, onClose }) {
  const [mode, setMode] = useState('add')
  const active = TABS.find((t) => t.id === mode)
  const ActiveForm = active.Form

  return (
    <div className="dialog-overlay" onClick={onClose}>
      <div className="dialog manage-members-dialog" onClick={(e) => e.stopPropagation()}>
        <h2>Manage Members — {room.name}</h2>

        <div className="manage-members-tabs" role="tablist">
          {TABS.map((t) => (
            <button
              key={t.id}
              type="button"
              role="tab"
              aria-selected={mode === t.id}
              className={`manage-members-tab${mode === t.id ? ' manage-members-tab-active' : ''}`}
              onClick={() => setMode(t.id)}
            >
              {t.label}
            </button>
          ))}
        </div>

        <ActiveForm room={room} />

        <div className="dialog-actions manage-members-footer">
          <button type="button" className="dialog-cancel" onClick={onClose}>
            Close
          </button>
        </div>
      </div>
    </div>
  )
}
