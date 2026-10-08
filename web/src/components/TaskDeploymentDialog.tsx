import { LoaderCircle, Network, Rocket, Shuffle, X } from 'lucide-react'
import { useEffect, useState, type FormEvent } from 'react'
import type { DeployTaskInput, DeliveryInfo } from '../types'

interface TaskDeploymentDialogProps {
  open: boolean
  deployment?: DeliveryInfo
  deploying: boolean
  error: string | null
  onClose: () => void
  onDeploy: (input: DeployTaskInput) => void
}

// TaskDeploymentDialog collects optional localhost ports without using browser
// prompts. Blank fields deliberately map to zero so the backend allocates ports.
export function TaskDeploymentDialog({
  open,
  deployment,
  deploying,
  error,
  onClose,
  onDeploy,
}: TaskDeploymentDialogProps) {
  const [frontendPort, setFrontendPort] = useState('')
  const [backendPort, setBackendPort] = useState('')
  const [randomized, setRandomized] = useState(false)

  // Reset to the persisted deployment when opening for a redeploy; the user can
  // clear either field or choose the random shortcut for fresh port allocation.
  useEffect(() => {
    if (!open) return
    setFrontendPort(deployment?.frontendPort ? String(deployment.frontendPort) : '')
    setBackendPort(deployment?.backendPort ? String(deployment.backendPort) : '')
    setRandomized(false)
  }, [deployment?.backendPort, deployment?.frontendPort, open])

  // submitDeployment converts optional input text into the API's zero-is-random
  // contract and leaves server-side availability checks authoritative.
  function submitDeployment(event: FormEvent) {
    event.preventDefault()
    const frontend = frontendPort.trim() ? Number(frontendPort) : 0
    const backend = backendPort.trim() ? Number(backendPort) : 0
    if (!Number.isInteger(frontend) || !Number.isInteger(backend)) return
    onDeploy({ frontendPort: frontend, backendPort: backend })
  }

  if (!open) return null

  return (
    <div className="dialog-backdrop deployment-dialog-backdrop" role="presentation" onMouseDown={(event) => event.target === event.currentTarget && !deploying && onClose()}>
      <form className="dialog-card deployment-dialog" role="dialog" aria-modal="true" aria-labelledby="deployment-dialog-title" onSubmit={submitDeployment}>
        <header>
          <span><Rocket size={20} /></span>
          <div><h2 id="deployment-dialog-title">{deployment?.status === 'RUNNING' ? '重新部署任务' : '部署已完成任务'}</h2><p>启动任务生成的前端与后端项目，端口留空将自动选择。</p></div>
          <button className="icon-button" type="button" disabled={deploying} onClick={onClose} aria-label="关闭部署设置"><X size={18} /></button>
        </header>

        <div className="deployment-port-grid">
          <label>
            <span><Network size={14} />前端端口</span>
            <input type="number" min="1" max="65535" value={frontendPort} disabled={deploying} onChange={(event) => { setFrontendPort(event.target.value); setRandomized(false) }} placeholder="随机端口" />
          </label>
          <label>
            <span><Network size={14} />后端端口</span>
            <input type="number" min="1" max="65535" value={backendPort} disabled={deploying} onChange={(event) => { setBackendPort(event.target.value); setRandomized(false) }} placeholder="随机端口" />
          </label>
        </div>

        <button className={`deployment-random-button ${randomized ? 'is-randomized' : ''}`} type="button" disabled={deploying} onClick={() => {
          const frontend = randomDeploymentPort()
          let backend = randomDeploymentPort()
          while (backend === frontend) backend = randomDeploymentPort()
          setFrontendPort(String(frontend))
          setBackendPort(String(backend))
          setRandomized(true)
        }}>
          <Shuffle size={14} />{randomized ? '已生成随机端口，可继续部署' : '两个端口都随机分配'}
        </button>
        {error && <div className="page-error deployment-error"><span>{error}</span></div>}

        <footer>
          <button className="button secondary" type="button" disabled={deploying} onClick={onClose}>取消</button>
          <button className="button primary" type="submit" disabled={deploying}>
            {deploying ? <LoaderCircle size={15} /> : <Rocket size={15} />}
            {deploying ? '正在启动前后端…' : deployment?.status === 'RUNNING' ? '重新部署' : '开始部署'}
          </button>
        </footer>
      </form>
    </div>
  )
}

// randomDeploymentPort produces a visible high-port suggestion with browser
// cryptography; the backend remains authoritative for availability validation.
function randomDeploymentPort(): number {
  const value = new Uint32Array(1)
  window.crypto.getRandomValues(value)
  return 20000 + (value[0] % 30000)
}
