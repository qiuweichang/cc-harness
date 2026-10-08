import { FileSliders, LoaderCircle, Plus, Save, Search, Trash2, X } from 'lucide-react'
import { useEffect, useMemo, useState, type FormEvent } from 'react'
import {
  createConfigurationSet, deleteConfigurationSet, listConfigurationSets, updateConfigurationSet,
} from '../api'
import type { ConfigurationEntry, ConfigurationSet, ConfigurationSetInput } from '../types'
import { useAppDialog } from './AppDialogProvider'

const emptyEntry = (): ConfigurationEntry => ({ id: '', name: '', content: '' })

// ConfigurationPage manages reusable named configuration documents and keeps
// editing local until the user explicitly saves the complete collection.
export function ConfigurationPage() {
  const appDialog = useAppDialog()
  const [sets, setSets] = useState<ConfigurationSet[]>([])
  const [selectedId, setSelectedId] = useState<string | null>(null)
  const [query, setQuery] = useState('')
  const [form, setForm] = useState<ConfigurationSetInput>({ name: '', description: '', entries: [emptyEntry()] })
  const [loading, setLoading] = useState(true)
  const [saving, setSaving] = useState(false)
  const [error, setError] = useState<string | null>(null)

  const filteredSets = useMemo(() => {
    const normalized = query.trim().toLowerCase()
    if (!normalized) return sets
    return sets.filter((set) => `${set.name} ${set.description} ${set.entries.map((entry) => entry.name).join(' ')}`.toLowerCase().includes(normalized))
  }, [query, sets])

  // Load all sets once when the management tab is first mounted.
  useEffect(() => {
    let cancelled = false
    listConfigurationSets()
      .then((items) => {
        if (cancelled) return
        setSets(items)
        if (items[0]) selectSet(items[0])
      })
      .catch((loadError) => !cancelled && setError(loadError instanceof Error ? loadError.message : '无法读取配置集'))
      .finally(() => !cancelled && setLoading(false))
    return () => {
      cancelled = true
    }
  }, [])

  // selectSet copies server data into the editor so unsaved typing never mutates list state.
  function selectSet(set: ConfigurationSet) {
    setSelectedId(set.id)
    setForm({
      name: set.name,
      description: set.description,
      entries: set.entries.map((entry) => ({ ...entry })),
    })
    setError(null)
  }

  // beginCreate opens a clean editor with one configuration row ready for input.
  function beginCreate() {
    setSelectedId(null)
    setForm({ name: '', description: '', entries: [emptyEntry()] })
    setError(null)
  }

  // updateEntry replaces one row immutably to keep React rendering predictable.
  function updateEntry(index: number, patch: Partial<ConfigurationEntry>) {
    setForm((current) => ({
      ...current,
      entries: current.entries.map((entry, entryIndex) => entryIndex === index ? { ...entry, ...patch } : entry),
    }))
  }

  // removeEntry deletes one row while retaining at least one editable configuration.
  function removeEntry(index: number) {
    setForm((current) => {
      const entries = current.entries.filter((_, entryIndex) => entryIndex !== index)
      return { ...current, entries: entries.length > 0 ? entries : [emptyEntry()] }
    })
  }

  // saveSet creates or updates the complete configuration collection in one request.
  async function saveSet(event: FormEvent) {
    event.preventDefault()
    setSaving(true)
    setError(null)
    try {
      const saved = selectedId
        ? await updateConfigurationSet(selectedId, form)
        : await createConfigurationSet(form)
      setSets((current) => {
        const exists = current.some((set) => set.id === saved.id)
        return exists ? current.map((set) => set.id === saved.id ? saved : set) : [...current, saved]
      })
      selectSet(saved)
    } catch (saveError) {
      setError(saveError instanceof Error ? saveError.message : '配置集保存失败')
    } finally {
      setSaving(false)
    }
  }

  // removeSet confirms the destructive catalog operation through the shared dialog.
  async function removeSet() {
    if (!selectedId) return
    const selected = sets.find((set) => set.id === selectedId)
    const accepted = await appDialog.confirm({
      title: '删除配置集',
      message: `确定删除配置集“${selected?.name ?? selectedId}”吗？已被任务使用的配置集不能删除。`,
      confirmText: '删除配置集',
      tone: 'danger',
    })
    if (!accepted) return
    setSaving(true)
    setError(null)
    try {
      await deleteConfigurationSet(selectedId)
      const next = sets.filter((set) => set.id !== selectedId)
      setSets(next)
      if (next[0]) selectSet(next[0])
      else beginCreate()
    } catch (deleteError) {
      setError(deleteError instanceof Error ? deleteError.message : '配置集删除失败')
    } finally {
      setSaving(false)
    }
  }

  return (
    <section className="configuration-page">
      <header className="configuration-page-header">
        <div><span className="eyebrow">CONFIGURATION SETS</span><h1>配置管理</h1></div>
        <button className="button primary" type="button" onClick={beginCreate}><Plus size={15} />新增配置集</button>
      </header>

      {error && <div className="page-error configuration-page-error"><span>{error}</span><button type="button" onClick={() => setError(null)}><X size={14} /></button></div>}

      <div className="configuration-layout">
        <aside className="configuration-set-list">
          <label className="configuration-search"><Search size={14} /><input value={query} onChange={(event) => setQuery(event.target.value)} placeholder="搜索配置集或配置名称" /></label>
          {loading ? <div className="configuration-empty"><LoaderCircle className="spinning-icon" size={18} />正在读取配置集…</div> : filteredSets.length === 0 ? <div className="configuration-empty"><FileSliders size={21} />暂无配置集</div> : filteredSets.map((set) => (
            <button className={selectedId === set.id ? 'active' : ''} type="button" key={set.id} onClick={() => selectSet(set)}>
              <span><strong>{set.name}</strong><small>{set.description || '未填写说明'}</small></span>
              <em>{set.entries.length}</em>
            </button>
          ))}
        </aside>

        <form className="configuration-editor" onSubmit={saveSet}>
          <header><div><span className="eyebrow">{selectedId ? 'EDIT SET' : 'NEW SET'}</span><h2>{selectedId ? '编辑配置集' : '新增配置集'}</h2></div>{selectedId && <button className="icon-button danger" type="button" disabled={saving} onClick={() => void removeSet()} title="删除配置集"><Trash2 size={16} /></button>}</header>
          <div className="configuration-meta-grid">
            <label><span>配置集名称</span><input required value={form.name} onChange={(event) => setForm({ ...form, name: event.target.value })} placeholder="例如：测试环境" /></label>
            <label><span>用途说明</span><input value={form.description} onChange={(event) => setForm({ ...form, description: event.target.value })} placeholder="说明环境、项目或使用范围" /></label>
          </div>
          <section className="configuration-entry-section">
            <header><div><strong>配置项</strong><small>按名称供智能体查询，内容保持原始格式。</small></div><button className="button secondary compact" type="button" onClick={() => setForm((current) => ({ ...current, entries: [...current.entries, emptyEntry()] }))}><Plus size={13} />新增配置</button></header>
            <div className="configuration-entry-list">
              {form.entries.map((entry, index) => (
                <article key={entry.id || `new-${index}`}>
                  <header><label><span>配置名称</span><input required value={entry.name} onChange={(event) => updateEntry(index, { name: event.target.value })} placeholder="例如：redis" /></label><button className="icon-button danger" type="button" onClick={() => removeEntry(index)} title="移除配置"><Trash2 size={15} /></button></header>
                  <label><span>配置内容</span><textarea required value={entry.content} onChange={(event) => updateEntry(index, { content: event.target.value })} placeholder={'host: 192.168.1.10\nport: 6379\ndatabase: 1'} /></label>
                </article>
              ))}
            </div>
          </section>
          <footer><button className="button primary" type="submit" disabled={saving}>{saving ? <LoaderCircle className="spinning-icon" size={15} /> : <Save size={15} />}{saving ? '保存中…' : '保存配置集'}</button></footer>
        </form>
      </div>
    </section>
  )
}
