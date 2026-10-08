import { BookOpen, Bot, Check, ChevronDown, ExternalLink, Plus, Save, Scale, Search, Sparkles, Trash2, X } from 'lucide-react'
import { useEffect, useMemo, useState, type FormEvent } from 'react'
import { addAssetVersion, createAsset, deleteAsset, getAssetRelations, listAssets, updateAsset } from '../api'
import type { Asset, AssetInput, AssetKind } from '../types'
import { useAppDialog } from './AppDialogProvider'

interface AssetPageProps {
  kind: AssetKind
  title: string
}

const roleOptions = [
  'TASK_BUILDER', 'PROJECT_MANAGER', 'PRODUCT_MANAGER', 'ARCHITECT', 'UI_DESIGNER',
  'FRONTEND_ENGINEER', 'BACKEND_ENGINEER', 'TEST_ENGINEER', 'AUDITOR', 'CUSTOM',
]

const kindIcons = { agents: Bot, skills: Sparkles, rules: Scale, 'knowledge-bases': BookOpen }
const sourceFilters = [
  { value: 'ALL', label: '全部' },
  { value: 'SYSTEM', label: '系统默认' },
  { value: 'USER', label: '用户创建' },
  { value: 'AI', label: 'AI 创建' },
] as const

// emptyInput returns a complete controlled form for one catalog kind.
function emptyInput(kind: AssetKind): AssetInput {
  return {
    id: '', name: '', description: '', avatar: '', source: 'USER', role: kind === 'agents' ? 'CUSTOM' : '',
    enabled: true, version: '1.0.0', defaultSkillIds: [], config: kind === 'knowledge-bases' ? { type: 'LOCAL_DIRECTORY', path: '' } : {},
    body: kind === 'knowledge-bases' ? '# 知识库\n\n描述该知识源的用途和使用边界。' : '# 新资产\n\n请填写具体职责和执行要求。',
  }
}

// assetToInput converts API metadata into the shared edit form shape.
function assetToInput(asset: Asset): AssetInput {
  return {
    id: asset.id, name: asset.name, description: asset.description, avatar: asset.avatar ?? '', source: asset.source,
    role: asset.role ?? '', enabled: asset.enabled, version: asset.currentVersion,
    defaultSkillIds: asset.defaultSkillIds ?? [], config: asset.config ?? {}, body: asset.body,
  }
}

// AssetAvatar renders a configured agent image and falls back to the section icon.
function AssetAvatar({ asset, icon: Icon }: { asset: Asset; icon: typeof Bot }) {
  if (asset.avatar) return <img src={asset.avatar} alt="" />
  return <Icon size={16} />
}

// AssetPage implements list, create, edit, enable, version, relation, and delete operations.
export function AssetPage({ kind, title }: AssetPageProps) {
  const appDialog = useAppDialog()
  const [items, setItems] = useState<Asset[]>([])
  const [skills, setSkills] = useState<Asset[]>([])
  const [selectedId, setSelectedId] = useState<string | null>(null)
  const [query, setQuery] = useState('')
  const [sourceFilter, setSourceFilter] = useState<(typeof sourceFilters)[number]['value']>('ALL')
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)
  const [dialogOpen, setDialogOpen] = useState(false)
  const [editing, setEditing] = useState<Asset | null>(null)
  const [form, setForm] = useState<AssetInput>(() => emptyInput(kind))
  const [saving, setSaving] = useState(false)
  const [relations, setRelations] = useState<{ taskIds: string[]; agentIds: string[] }>({ taskIds: [], agentIds: [] })
  const [newVersion, setNewVersion] = useState('')
  const [newVersionBody, setNewVersionBody] = useState('')

  const selected = items.find((item) => item.id === selectedId) ?? null
  const Icon = kindIcons[kind]
  const filtered = useMemo(() => items.filter((item) => {
    const matchesQuery = `${item.name} ${item.description} ${item.id}`.toLowerCase().includes(query.toLowerCase())
    return matchesQuery && (sourceFilter === 'ALL' || item.source === sourceFilter)
  }), [items, query, sourceFilter])

  // load refreshes the selected catalog and skill options used by agent editing.
  async function load() {
    setLoading(true)
    setError(null)
    try {
      const [assets, skillAssets] = await Promise.all([listAssets(kind), kind === 'agents' ? listAssets('skills') : Promise.resolve([])])
      setItems(assets)
      setSkills(skillAssets)
      setSelectedId((current) => current && assets.some((item) => item.id === current) ? current : assets[0]?.id ?? null)
    } catch (loadError) {
      setError(loadError instanceof Error ? loadError.message : '无法读取资产目录')
    } finally {
      setLoading(false)
    }
  }

  // Initial loading follows the active asset kind.
  useEffect(() => { void load() }, [kind])

  // Relation data updates whenever selection changes.
  useEffect(() => {
    if (!selectedId) return
    void getAssetRelations(kind, selectedId).then(setRelations).catch(() => setRelations({ taskIds: [], agentIds: [] }))
  }, [kind, selectedId, items])

  // Keep the version editor initialized with the currently selected Markdown.
  useEffect(() => {
    setNewVersionBody(selected?.body ?? '')
  }, [selected?.id, selected?.currentVersion])

  // openCreate starts a clean user-owned asset form.
  function openCreate() {
    setEditing(null)
    setForm(emptyInput(kind))
    setDialogOpen(true)
  }

  // openEdit loads mutable fields for the selected asset.
  function openEdit() {
    if (!selected) return
    setEditing(selected)
    setForm(assetToInput(selected))
    setDialogOpen(true)
  }

  // submitForm persists a new asset or mutable metadata changes.
  async function submitForm(event: FormEvent) {
    event.preventDefault()
    setSaving(true)
    setError(null)
    try {
      const saved = editing ? await updateAsset(kind, editing.id, form) : await createAsset(kind, form)
      setDialogOpen(false)
      await load()
      setSelectedId(saved.id)
    } catch (saveError) {
      setError(saveError instanceof Error ? saveError.message : '保存失败')
    } finally {
      setSaving(false)
    }
  }

  // toggleEnabled changes whether future tasks may select the asset.
  async function toggleEnabled() {
    if (!selected) return
    try {
      const saved = await updateAsset(kind, selected.id, { ...assetToInput(selected), enabled: !selected.enabled })
      setItems((current) => current.map((item) => item.id === saved.id ? saved : item))
    } catch (toggleError) {
      setError(toggleError instanceof Error ? toggleError.message : '启用状态更新失败')
    }
  }

  // switchVersion selects an existing immutable version through metadata update.
  async function switchVersion(version: string) {
    if (!selected) return
    try {
      const saved = await updateAsset(kind, selected.id, { ...assetToInput(selected), version })
      setItems((current) => current.map((item) => item.id === saved.id ? saved : item))
    } catch (versionError) {
      setError(versionError instanceof Error ? versionError.message : '版本切换失败')
    }
  }

  // publishVersion creates a new version from the current editor body.
  async function publishVersion() {
    if (!selected || !newVersion.trim()) return
    try {
      const saved = await addAssetVersion(kind, selected.id, newVersion.trim(), newVersionBody)
      setItems((current) => current.map((item) => item.id === saved.id ? saved : item))
      setNewVersion('')
    } catch (versionError) {
      setError(versionError instanceof Error ? versionError.message : '版本创建失败')
    }
  }

  // removeSelected deletes only user or AI assets with no task references.
  async function removeSelected() {
    if (!selected || selected.source === 'SYSTEM') return
    const accepted = await appDialog.confirm({
      title: `删除${title}`,
      message: `确定删除“${selected.name}”吗？此操作不可恢复。`,
      confirmText: '删除',
      tone: 'danger',
    })
    if (!accepted) return
    try {
      await deleteAsset(kind, selected.id)
      await load()
    } catch (deleteError) {
      setError(deleteError instanceof Error ? deleteError.message : '删除失败')
    }
  }

  // toggleDefaultSkill adds or removes a skill in the controlled agent form.
  function toggleDefaultSkill(skillId: string) {
    setForm((current) => ({
      ...current,
      defaultSkillIds: current.defaultSkillIds.includes(skillId)
        ? current.defaultSkillIds.filter((id) => id !== skillId)
        : [...current.defaultSkillIds, skillId],
    }))
  }

  return (
    <div className="asset-page">
      <header className="asset-page-header">
        <div><span className="eyebrow">CATALOG</span><h1>{title}</h1></div>
        <button className="button primary compact" type="button" onClick={openCreate}><Plus size={16} />新增{title}</button>
      </header>

      {error && <div className="page-error"><span>{error}</span><button type="button" onClick={() => setError(null)}><X size={14} /></button></div>}

      <div className="asset-layout">
        <aside className="asset-list-panel">
          <div className="asset-search"><Search size={15} /><input value={query} onChange={(event) => setQuery(event.target.value)} placeholder={`搜索${title}`} /></div>
          <div className="asset-source-filters">{sourceFilters.map((filter) => <button className={sourceFilter === filter.value ? 'active' : ''} type="button" key={filter.value} onClick={() => setSourceFilter(filter.value)}>{filter.label}</button>)}</div>
          <div className="asset-list-scroll">
            {loading ? <p className="muted-block">正在加载…</p> : filtered.map((item) => (
              <button className={`asset-list-item ${selectedId === item.id ? 'selected' : ''}`} key={item.id} type="button" onClick={() => setSelectedId(item.id)}>
                <span className="asset-list-icon"><AssetAvatar asset={item} icon={Icon} /></span>
                <span><strong>{item.name}</strong><small>{item.description || item.id}</small></span>
                <i className={item.enabled ? 'online' : ''} />
              </button>
            ))}
          </div>
        </aside>

        <main className="asset-detail-panel">
          {selected ? (
            <>
              <div className="asset-detail-heading">
                <span className="asset-hero-icon"><AssetAvatar asset={selected} icon={Icon} /></span>
                <div><h2>{selected.name}</h2><p>{selected.id} · {sourceFilters.find((filter) => filter.value === selected.source)?.label ?? selected.source}</p></div>
                <button className={`toggle ${selected.enabled ? 'on' : ''}`} type="button" onClick={toggleEnabled} aria-label="启用或禁用"><span /></button>
                <button className="button secondary compact" type="button" onClick={openEdit}>编辑</button>
                {selected.source !== 'SYSTEM' && <button className="icon-button danger" type="button" onClick={removeSelected} aria-label="删除"><Trash2 size={17} /></button>}
              </div>

              <p className="asset-description">{selected.description}</p>
              <div className="asset-meta-grid">
                <div><span>当前版本</span><div className="version-select"><span>v</span><select value={selected.currentVersion} onChange={(event) => void switchVersion(event.target.value)}>{selected.versions.map((version) => <option key={version}>{version}</option>)}</select><ChevronDown size={14} /></div></div>
                {kind === 'agents' && <div><span>角色</span><strong>{selected.role}</strong></div>}
                <div><span>关联任务</span><strong>{relations.taskIds.length}</strong></div>
                {kind === 'skills' && <div><span>关联智能体</span><strong>{relations.agentIds.length}</strong></div>}
              </div>

              {(relations.taskIds.length > 0 || relations.agentIds.length > 0) && <section className="asset-subsection"><h3>关联关系</h3><div className="tag-list">{relations.taskIds.map((id) => <span key={`task-${id}`}>任务 {id}</span>)}{relations.agentIds.map((id) => <span key={`agent-${id}`}>智能体 {id}</span>)}</div></section>}

              {kind === 'agents' && <section className="asset-subsection"><h3>默认技能</h3><div className="tag-list">{(selected.defaultSkillIds ?? []).map((id) => <span key={id}>{skills.find((skill) => skill.id === id)?.name ?? id}</span>)}</div></section>}
              {kind === 'knowledge-bases' && <section className="asset-subsection"><h3>连接配置</h3><pre>{JSON.stringify(selected.config ?? {}, null, 2)}</pre></section>}

              <section className="asset-subsection body-section">
                <div className="subsection-heading"><h3>本体 Markdown</h3><div><span>{selected.body.length} 字符</span><a className="button secondary compact" href={`/api/v1/assets/${kind}/${selected.id}/content.md`} target="_blank" rel="noreferrer"><ExternalLink size={14} />打开本体文件</a></div></div>
                <pre className="markdown-source">{selected.body}</pre>
              </section>

              <section className="asset-subsection version-publisher">
                <h3>发布新版本</h3>
                <textarea className="version-body-editor" value={newVersionBody} onChange={(event) => setNewVersionBody(event.target.value)} />
                <div><input value={newVersion} onChange={(event) => setNewVersion(event.target.value)} placeholder="例如 1.1.0" /><button className="button secondary compact" type="button" onClick={() => void publishVersion()}><Plus size={15} />发布并切换</button></div>
              </section>
            </>
          ) : <div className="detail-empty"><Icon size={24} /><strong>还没有{title}</strong><button className="button secondary" type="button" onClick={openCreate}><Plus size={15} />立即创建</button></div>}
        </main>
      </div>

      {dialogOpen && (
        <div className="dialog-backdrop" role="presentation">
          <form className="dialog-card asset-dialog" onSubmit={submitForm}>
            <div className="dialog-header"><div><span className="eyebrow">{editing ? 'EDIT ASSET' : 'NEW ASSET'}</span><h2>{editing ? `编辑${title}` : `新增${title}`}</h2></div><button className="icon-button" type="button" onClick={() => setDialogOpen(false)}><X size={18} /></button></div>
            <div className="form-grid two">
              <label><span>标识</span><input disabled={Boolean(editing)} value={form.id} onChange={(event) => setForm({ ...form, id: event.target.value })} placeholder="lowercase-id" /></label>
              <label><span>名称</span><input value={form.name} onChange={(event) => setForm({ ...form, name: event.target.value })} /></label>
            </div>
            <label className="form-field"><span>描述</span><input value={form.description} onChange={(event) => setForm({ ...form, description: event.target.value })} /></label>
            {kind === 'agents' && <label className="form-field"><span>头像 URL</span><input value={form.avatar} onChange={(event) => setForm({ ...form, avatar: event.target.value })} placeholder="https://... 或 data:image/..." /></label>}
            {!editing && <label className="form-field"><span>来源类型</span><select value={form.source} onChange={(event) => setForm({ ...form, source: event.target.value })}><option value="USER">用户创建</option><option value="AI">AI 创建/保存</option></select></label>}
            {kind === 'agents' && <label className="form-field"><span>角色</span><select value={form.role} onChange={(event) => setForm({ ...form, role: event.target.value })}>{roleOptions.map((role) => <option key={role}>{role}</option>)}</select></label>}
            {!editing && <label className="form-field"><span>初始版本</span><input value={form.version} onChange={(event) => setForm({ ...form, version: event.target.value })} /></label>}
            {kind === 'knowledge-bases' && <div className="form-grid two"><label><span>类型</span><select value={String(form.config.type ?? 'LOCAL_DIRECTORY')} onChange={(event) => setForm({ ...form, config: { type: event.target.value, [event.target.value === 'MCP' ? 'server' : 'path']: '' } })}><option>LOCAL_DIRECTORY</option><option>MCP</option></select></label><label><span>{form.config.type === 'MCP' ? 'MCP 服务名' : '本地目录'}</span><input value={String(form.config.type === 'MCP' ? form.config.server ?? '' : form.config.path ?? '')} onChange={(event) => setForm({ ...form, config: { ...form.config, [form.config.type === 'MCP' ? 'server' : 'path']: event.target.value } })} /></label></div>}
            {kind === 'agents' && <div className="form-field"><span>默认技能</span><div className="check-grid">{skills.map((skill) => <button className={form.defaultSkillIds.includes(skill.id) ? 'checked' : ''} type="button" key={skill.id} onClick={() => toggleDefaultSkill(skill.id)}>{form.defaultSkillIds.includes(skill.id) && <Check size={13} />}{skill.name}</button>)}</div></div>}
            {!editing && <label className="form-field"><span>本体 Markdown</span><textarea className="code-editor" value={form.body} onChange={(event) => setForm({ ...form, body: event.target.value })} /></label>}
            <div className="dialog-actions"><button className="button secondary" type="button" onClick={() => setDialogOpen(false)}>取消</button><button className="button primary" type="submit" disabled={saving || !form.name.trim() || (!editing && !form.id.trim())}><Save size={15} />{saving ? '保存中…' : '保存'}</button></div>
          </form>
        </div>
      )}
    </div>
  )
}
