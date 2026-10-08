import { ExternalLink, FileText, LoaderCircle } from 'lucide-react'
import { useEffect, useMemo, useRef, useState } from 'react'
import { listTaskSessionEvents, listTaskSessions, openTaskFile } from '../api'
import type { RuntimeEvent } from '../types'
import { presentConversationEvents, type RuntimePresentation } from './runtimePresentation'

interface TaskConversationHistoryProps {
  taskId: string
  memberId: string
  // agentName labels assistant messages with the phase role visible to users.
  agentName: string
  // activeSession and liveEvents bypass the slower archive discovery cycle so
  // streamed assistant text appears as soon as the workspace poll receives it.
  activeSession: { id: string; startedAt: string } | null
  liveEvents: RuntimeEvent[]
}

interface ArchivedEvent {
  sessionId: string
  sessionStartedAt: string
  event: RuntimeEvent
}

// TaskConversationHistory merges every archived session for the phase companion
// into one readable conversation without reconnecting to an old agent process.
export function TaskConversationHistory({ taskId, memberId, agentName, activeSession, liveEvents }: TaskConversationHistoryProps) {
  const [records, setRecords] = useState<ArchivedEvent[]>([])
  const [loading, setLoading] = useState(true)
  const [openingDocument, setOpeningDocument] = useState('')
  const [documentError, setDocumentError] = useState('')
  // Session cursors and event keys are transient polling data; refs prevent them
  // from causing a render or rebuilding the timer for every incoming event.
  const sequenceBySessionRef = useRef(new Map<string, number>())
  const eventKeysRef = useRef(new Set<string>())
  const combinedRecords = useMemo(() => {
    const indexed = new Map(records.map((record) => [`${record.sessionId}:${record.event.sequence}`, record]))
    if (activeSession) {
      for (const event of liveEvents) {
        const key = `${activeSession.id}:${event.sequence}`
        indexed.set(key, { sessionId: activeSession.id, sessionStartedAt: activeSession.startedAt, event })
      }
    }
    return Array.from(indexed.values()).sort(compareArchivedEvents)
  }, [activeSession, liveEvents, records])
  const messages = useMemo(() => buildConversationMessages(combinedRecords), [combinedRecords])

  // Fetch all relevant sessions in parallel, then append only unseen events. A
  // single timer covers both session discovery and history growth.
  useEffect(() => {
    setRecords([])
    setLoading(true)
    sequenceBySessionRef.current = new Map()
    eventKeysRef.current = new Set()
    let cancelled = false

    const refreshHistory = async () => {
      try {
        const sessions = (await listTaskSessions(taskId)).filter((session) => sessionBelongsToMember(session.memberId, memberId))
        const eventGroups = await Promise.all(sessions.map(async (session) => {
          try {
            return {
              session,
              events: await listTaskSessionEvents(taskId, session.id, sequenceBySessionRef.current.get(session.id) ?? 0),
            }
          } catch {
            return { session, events: [] as RuntimeEvent[] }
          }
        }))
        if (cancelled) return
        const additions: ArchivedEvent[] = []
        for (const group of eventGroups) {
          for (const event of group.events) {
            const key = `${group.session.id}:${event.sequence}`
            if (eventKeysRef.current.has(key)) continue
            eventKeysRef.current.add(key)
            additions.push({ sessionId: group.session.id, sessionStartedAt: group.session.startedAt, event })
          }
          const lastEvent = group.events[group.events.length - 1]
          if (lastEvent) sequenceBySessionRef.current.set(group.session.id, lastEvent.sequence)
        }
        if (additions.length > 0) {
          setRecords((current) => [...current, ...additions].sort(compareArchivedEvents))
        }
      } catch {
        // The task-level error surface handles API failures; retain the last
        // durable conversation instead of clearing it during a transient outage.
      } finally {
        if (!cancelled) setLoading(false)
      }
    }

    void refreshHistory()
    const timer = window.setInterval(() => void refreshHistory(), 1500)
    return () => {
      cancelled = true
      window.clearInterval(timer)
    }
  }, [memberId, taskId])

  // openConversationDocument delegates path validation and local file navigation
  // to the backend so chat output never constructs an unsafe filesystem command.
  async function openConversationDocument(path: string) {
    setOpeningDocument(path)
    setDocumentError('')
    try {
      await openTaskFile(taskId, path)
    } catch (openError) {
      setDocumentError(openError instanceof Error ? openError.message : '无法打开文档')
    } finally {
      setOpeningDocument('')
    }
  }

  if (loading && messages.length === 0) return <div className="conversation-history-loading"><LoaderCircle size={18} /></div>
  if (messages.length === 0) return <div className="chat-pristine" />
  return <div className="conversation-history">
    {messages.map((message) => {
      const documentPaths = message.kind === 'assistant' ? extractTechnicalDocumentPaths(message.text) : []
      return (
        <article className={`conversation-message message-${message.kind}`} key={message.id}>
          <strong>{message.kind === 'user' ? '我' : message.kind === 'error' ? '异常' : agentName}</strong>
          <p>{message.text}</p>
          {documentPaths.length > 0 && <div className="conversation-document-list">
            {documentPaths.map((path) => (
              <button type="button" key={path} disabled={openingDocument === path} onClick={() => void openConversationDocument(path)} title="打开原文件">
                {openingDocument === path ? <LoaderCircle className="spinning-icon" size={14} /> : <FileText size={14} />}
                <span>{documentDisplayName(path)}</span>
                <ExternalLink size={12} />
              </button>
            ))}
          </div>}
        </article>
      )
    })}
    {documentError && <div className="conversation-document-error" role="alert">{documentError}</div>}
  </div>
}

// extractTechnicalDocumentPaths recognizes managed document outputs without
// turning arbitrary URLs, source files, or generated project folders into links.
function extractTechnicalDocumentPaths(text: string): string[] {
  const pattern = /(?:^|[\s`"'（(：:])((?:docs[\\/])[^`\n\r"'<>|*?，。；;：:）)]+?\.(?:md|pdf|docx?|xlsx?|pptx?|html?|txt|json|ya?ml))(?=$|[\s`"'，。；;：:）)])/gim
  const paths = new Set<string>()
  for (const match of text.matchAll(pattern)) {
    const path = match[1]?.trim()
    if (path) paths.add(path)
  }
  return Array.from(paths)
}

// documentDisplayName keeps document chips compact while their title and backend
// request retain the complete task-relative path.
function documentDisplayName(path: string): string {
  return path.split(/[\\/]/).at(-1) ?? path
}

// buildConversationMessages keeps user-visible dialogue and errors while leaving
// internal thinking and tool diagnostics in the dedicated background inspector.
function buildConversationMessages(records: ArchivedEvent[]): RuntimePresentation[] {
  const sessions = new Map<string, ArchivedEvent[]>()
  for (const record of records) sessions.set(record.sessionId, [...(sessions.get(record.sessionId) ?? []), record])
  return Array.from(sessions.values()).flatMap((sessionRecords) => {
    const sessionId = sessionRecords[0]?.sessionId ?? ''
    return presentConversationEvents(sessionRecords.map((record) => record.event))
      .filter((entry) => entry.kind === 'user' || entry.kind === 'assistant' || entry.kind === 'error')
      .map((entry) => ({ ...entry, id: `${sessionId}:${entry.id}` }))
  })
}

// sessionBelongsToMember includes legacy sessions and team-builder rebuild sessions
// in the same phase history while keeping unrelated members out.
function sessionBelongsToMember(sessionMemberId: string, memberId: string): boolean {
  if (memberId === 'task-builder') return sessionMemberId.startsWith('task-builder')
  return sessionMemberId === memberId || sessionMemberId === `chat:${memberId}`
}

// compareArchivedEvents preserves session chronology and each session's monotonic
// sequence even when archived log lines share the same file timestamp.
function compareArchivedEvents(left: ArchivedEvent, right: ArchivedEvent): number {
  const sessionOrder = Date.parse(left.sessionStartedAt) - Date.parse(right.sessionStartedAt)
  if (sessionOrder !== 0) return sessionOrder
  if (left.sessionId !== right.sessionId) return left.sessionId.localeCompare(right.sessionId)
  return left.event.sequence - right.event.sequence
}
