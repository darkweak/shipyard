import { Ship } from 'lucide-react'
import { useState, type FormEvent } from 'react'
import { api } from '@/lib/api'
import { Button, ErrorBox, Input } from '@/components/ui'

export function LoginPage({ onLogin }: { onLogin: () => void }) {
  const [token, setToken] = useState('')
  const [error, setError] = useState('')
  const [busy, setBusy] = useState(false)

  const submit = async (e: FormEvent) => {
    e.preventDefault()
    setBusy(true)
    setError('')
    try {
      await api.login(token)
      onLogin()
    } catch (err) {
      setError((err as Error).message)
    } finally {
      setBusy(false)
    }
  }

  return (
    <div className="grid min-h-screen place-items-center px-4">
      <form onSubmit={submit} className="w-full max-w-sm space-y-5 rounded-2xl border border-border bg-panel p-7">
        <div className="flex items-center gap-2.5">
          <span className="grid size-9 place-items-center rounded-xl bg-accent text-accent-fg">
            <Ship className="size-5" />
          </span>
          <div>
            <h1 className="font-semibold tracking-tight">Shipyard</h1>
            <p className="text-xs text-muted">Deploy git repositories as containers</p>
          </div>
        </div>
        <label className="block space-y-1.5">
          <span className="text-sm font-medium">Admin token</span>
          <Input type="password" autoFocus value={token} onChange={(e) => setToken(e.target.value)} placeholder="SHIPYARD_TOKEN" />
        </label>
        <ErrorBox>{error}</ErrorBox>
        <Button variant="primary" className="w-full" loading={busy} disabled={!token}>
          Sign in
        </Button>
      </form>
    </div>
  )
}
