import type { RuntimeEvent } from '../types'

export type RuntimePresentationKind = 'user' | 'assistant' | 'thinking' | 'tool' | 'system' | 'error'

export interface RuntimePresentation {
  id: string
  kind: RuntimePresentationKind
  label: string
  text: string
  createdAt: string
}

// presentRuntimeEvents converts Claude stream envelopes into readable Chinese
// entries. Partial token deltas are ignored because complete assistant blocks are
// emitted separately and are substantially easier to read.
export function presentRuntimeEvents(events: RuntimeEvent[]): RuntimePresentation[] {
  const entries = events.flatMap((event) => presentRuntimeEvent(event))
  return entries.filter((entry, index) => {
    if (entry.label !== '执行结果') return true
    const normalized = normalizePresentationText(entry.text)
    return !entries.slice(Math.max(0, index - 4), index)
      .some((previous) => previous.kind === 'assistant' && normalizePresentationText(previous.text) === normalized)
  })
}

// normalizePresentationText compares complete assistant output with the protocol
// result envelope so an identical final answer is shown only once.
function normalizePresentationText(value: string): string {
  return value.replace(/\s+/g, ' ').trim()
}

// presentConversationEvents removes the hidden context-wakeup turn while keeping
// later user dialogue from the same persistent session. Background diagnostics
// continue to use presentRuntimeEvents and therefore retain the agent's wakeup output.
export function presentConversationEvents(events: RuntimeEvent[]): RuntimePresentation[] {
  let waitingForVisibleUser = false
  const entries: RuntimePresentation[] = []
  for (const event of events) {
    if (isAgentWakeupInput(event)) {
      waitingForVisibleUser = true
      continue
    }
    if (waitingForVisibleUser) {
      if (event.type !== 'user_input') continue
      waitingForVisibleUser = false
    }
    if (event.type === 'assistant_delta') {
      const text = readString(event.payload, 'text')
      if (!text) continue
      const previous = entries[entries.length - 1]
      if (previous?.kind === 'assistant' && previous.label === '实时回复') {
        previous.text += text
      } else {
        entries.push({
          id: `${event.sequence}-stream`,
          kind: 'assistant',
          label: '实时回复',
          text,
          createdAt: event.createdAt,
        })
      }
      continue
    }
    for (const entry of presentRuntimeEvent(event)) {
      if (entry.kind === 'assistant') {
        const streamIndex = findLatestStreamEntry(entries)
        if (streamIndex >= 0) {
          const streamed = normalizePresentationText(entries[streamIndex].text)
          const complete = normalizePresentationText(entry.text)
          if (streamed && (complete.includes(streamed) || streamed.includes(complete))) {
            entries[streamIndex] = { ...entry, id: entries[streamIndex].id }
            continue
          }
        }
      }
      entries.push(entry)
    }
  }
  return entries
}

// findLatestStreamEntry returns the nearest unfinished text stream so the final
// complete assistant block replaces it instead of producing a duplicate answer.
function findLatestStreamEntry(entries: RuntimePresentation[]): number {
  for (let index = entries.length - 1; index >= 0; index--) {
    if (entries[index].kind === 'user') return -1
    if (entries[index].kind === 'assistant' && entries[index].label === '实时回复') return index
  }
  return -1
}

// presentRuntimeEvent handles platform events and complete Claude message blocks
// without exposing protocol JSON to the user.
function presentRuntimeEvent(event: RuntimeEvent): RuntimePresentation[] {
  const base = { createdAt: event.createdAt }
  if (event.type === 'user_input') {
    if (isInternalWorkflowInput(event)) return []
    const content = extractUserInput(event.payload)
    return content ? [{ ...base, id: `${event.sequence}-user`, kind: 'user', label: '用户', text: content }] : []
  }
  if (event.type === 'stderr' || event.type === 'error') {
    const text = event.line || readString(event.payload, 'message') || '智能体发生未知错误'
    if (isClosedPipeNoise(text)) return []
    return [{ ...base, id: `${event.sequence}-error`, kind: 'error', label: '错误', text }]
  }
  if (event.type === 'session_started') {
    return [{ ...base, id: `${event.sequence}-start`, kind: 'system', label: '系统', text: '智能体会话已启动' }]
  }
  if (event.type === 'process_exit') {
    const code = readValue(event.payload, 'code')
    const intentional = Boolean(readValue(event.payload, 'intentional'))
    return [{ ...base, id: `${event.sequence}-exit`, kind: 'system', label: '系统', text: intentional ? '智能体会话已归档' : code === 0 ? '智能体会话已正常结束' : `智能体会话已结束，退出码：${formatValue(code)}` }]
  }
  if (event.type === 'permission_request') {
    const request = readObject(event.payload, 'request')
    const toolName = readString(request, 'tool_name') || readString(request, 'toolName') || '工具'
    return [{ ...base, id: `${event.sequence}-permission`, kind: 'tool', label: '权限请求', text: `请求使用：${toolName}` }]
  }
  if (event.type !== 'claude_event') return []

  const payload = asRecord(event.payload)
  const payloadType = readString(payload, 'type')
  if (payloadType === 'system' && readString(payload, 'subtype') === 'init') {
    const model = readString(payload, 'model')
    return [{ ...base, id: `${event.sequence}-init`, kind: 'system', label: '系统', text: model ? `智能体已初始化 · ${model}` : '智能体已初始化' }]
  }
  if (payloadType === 'result') {
    const result = readString(payload, 'result')
    const failed = Boolean(readValue(payload, 'is_error'))
    if (!result) return []
    return [{ ...base, id: `${event.sequence}-result`, kind: failed ? 'error' : 'system', label: failed ? '执行失败' : '执行结果', text: result }]
  }
  // Claude's --replay-user-messages envelope duplicates the durable user_input
  // event emitted by the runtime manager, so only that canonical local record is
  // rendered as a user message.
  if (payloadType !== 'assistant') return []

  const message = readObject(payload, 'message')
  const content = readValue(message, 'content')
  if (typeof content === 'string') {
    const text = stripPromptContext(content)
    return text ? [{ ...base, id: `${event.sequence}-message`, kind: 'assistant', label: '智能体', text }] : []
  }
  if (!Array.isArray(content)) return []
  return content.flatMap((block, index) => presentContentBlock(event, asRecord(block), index))
}

// isAgentWakeupInput detects the private prompt that starts one context-recovery
// turn. The marker is never surfaced as an ordinary user question.
function isAgentWakeupInput(event: RuntimeEvent): boolean {
  if (event.type !== 'user_input') return false
  return readString(event.payload, 'text').includes('__CC_HARDNESS_RESTORE__')
}

// isInternalWorkflowInput hides backend dispatch instructions from the user
// conversation while leaving the agent's progress and final answer visible.
function isInternalWorkflowInput(event: RuntimeEvent): boolean {
  if (event.type !== 'user_input') return false
  const text = stripPromptContext(readString(event.payload, 'text'))
  return text.includes('__CC_HARDNESS_WORKFLOW__') ||
    text.startsWith('执行工作流节点：') ||
    // Tasks created by older versions persisted this project-manager bootstrap
    // as user input. It is runtime initialization, not a question from the user.
    text.startsWith('任务已经开始。请读取 .cc-hardness/team.json')
}

// isClosedPipeNoise defensively suppresses historical Windows shutdown messages
// that may remain in old task logs created before backend filtering was added.
function isClosedPipeNoise(text: string): boolean {
  const normalized = text.toLowerCase()
  return normalized.includes('file already closed') ||
    normalized.includes('pipe has been ended') ||
    normalized.includes('read |0')
}

// presentContentBlock maps complete Claude content blocks to conversation cards.
function presentContentBlock(event: RuntimeEvent, block: Record<string, unknown>, index: number): RuntimePresentation[] {
  const type = readString(block, 'type')
  const common = { id: `${event.sequence}-${index}`, createdAt: event.createdAt }
  if (type === 'text') {
    const text = readString(block, 'text')
    return text ? [{ ...common, kind: 'assistant', label: '智能体', text }] : []
  }
  if (type === 'thinking') {
    const thinking = readString(block, 'thinking')
    return thinking ? [{ ...common, kind: 'thinking', label: '思考过程', text: thinking }] : []
  }
  if (type === 'tool_use') {
    const name = readString(block, 'name') || '工具'
    const input = readValue(block, 'input')
    const detail = formatValue(input)
    return [{ ...common, kind: 'tool', label: '工具调用', text: detail ? `${name}\n${detail}` : name }]
  }
  if (type === 'tool_result') {
    const content = readValue(block, 'content')
    return [{ ...common, kind: 'tool', label: '工具结果', text: formatValue(content) || '工具已返回结果' }]
  }
  return []
}

// extractUserInput supports both durable stdin envelopes and the compact live
// event emitted by the runtime manager.
function extractUserInput(payload: unknown): string {
  const compactText = readString(payload, 'text')
  if (compactText) return stripPromptContext(compactText)
  const message = readObject(payload, 'message')
  const content = readValue(message, 'content')
  return typeof content === 'string' ? stripPromptContext(content) : ''
}

// stripPromptContext hides agent initialization instructions while preserving the
// actual user message that follows the shared prompt marker.
function stripPromptContext(content: string): string {
  const marker = '## 用户消息'
  const markerIndex = content.lastIndexOf(marker)
  const visible = markerIndex < 0 ? content.trim() : content.slice(markerIndex + marker.length).replace(/^[\s:：-]+/, '').trim()
  return visible.startsWith('__CC_HARDNESS_RESTORE__') ? '' : visible
}

// formatValue turns structured tool arguments into readable key-value lines rather
// than exposing JSON braces, quotes, and protocol metadata.
function formatValue(value: unknown, depth = 0): string {
  if (value === undefined || value === null) return ''
  if (typeof value === 'string') return value
  if (typeof value === 'number' || typeof value === 'boolean') return String(value)
  if (Array.isArray(value)) return value.map((item) => formatValue(item, depth + 1)).filter(Boolean).join(depth === 0 ? '\n' : '、')
  const record = asRecord(value)
  return Object.entries(record)
    .map(([key, item]) => {
      const formatted = formatValue(item, depth + 1)
      return formatted ? `${friendlyKey(key)}：${formatted}` : ''
    })
    .filter(Boolean)
    .join('\n')
}

// friendlyKey translates frequent Claude tool fields while preserving unknown,
// domain-specific field names.
function friendlyKey(key: string): string {
  const labels: Record<string, string> = {
    command: '命令', description: '说明', file_path: '文件', path: '路径',
    pattern: '匹配内容', query: '查询', url: '地址', content: '内容',
  }
  return labels[key] ?? key
}

// asRecord narrows protocol payloads without unsafe property access.
function asRecord(value: unknown): Record<string, unknown> {
  return value && typeof value === 'object' && !Array.isArray(value) ? value as Record<string, unknown> : {}
}

// readObject returns one nested protocol object or an empty object.
function readObject(value: unknown, key: string): Record<string, unknown> {
  return asRecord(asRecord(value)[key])
}

// readValue returns one unknown protocol field.
function readValue(value: unknown, key: string): unknown {
  return asRecord(value)[key]
}

// readString returns one string protocol field without coercing objects.
function readString(value: unknown, key: string): string {
  const field = readValue(value, key)
  return typeof field === 'string' ? field : ''
}
