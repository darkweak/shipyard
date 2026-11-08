import { CheckCircle2 } from 'lucide-react'
import { useEffect, useState, type FormEvent } from 'react'
import { api } from '@/lib/api'
import { useAsync } from '@/lib/hooks'
import { Button, Card, ErrorBox, fromRows, KeyValueEditor, PageHeader, Spinner, toRows } from '@/components/ui'

export function SettingsPage() {
  const { data, error: loadError } = useAsync(api.settings, [])
  const { data: sys } = useAsync(api.system, [])
  const swarm = sys?.target === 'swarm'
  const [env, setEnv] = useState<[string, string][]>([])
  const [labels, setLabels] = useState<[string, string][]>([])
  const [saving, setSaving] = useState(false)
  const [saved, setSaved] = useState(false)
  const [error, setError] = useState('')

  useEffect(() => {
    if (!data) return
    setEnv(toRows(data.defaultEnv))
    setLabels(toRows(data.defaultLabels))
  }, [data])

  const submit = async (e: FormEvent) => {
    e.preventDefault()
    setSaving(true)
    setSaved(false)
    setError('')
    try {
      const s = await api.updateSettings({ defaultEnv: fromRows(env), defaultLabels: fromRows(labels) })
      setEnv(toRows(s.defaultEnv))
      setLabels(toRows(s.defaultLabels))
      setSaved(true)
    } catch (err) {
      setError((err as Error).message)
    } finally {
      setSaving(false)
    }
  }

  if (loadError) return <ErrorBox>{loadError}</ErrorBox>
  if (!data) return <Spinner />

  return (
    <form onSubmit={submit} className="space-y-5">
      <PageHeader title="Settings" subtitle="Defaults filled in the new project form. Changing them doesn't affect existing projects." />

      <Card title="Default environment variables">
        <KeyValueEditor rows={env} onChange={(r) => (setEnv(r), setSaved(false))} keyPlaceholder="TZ" valuePlaceholder="Europe/Paris" />
        <p className="mt-2 text-xs text-muted">Added to every new project's environment variables, where you can still edit or remove them.</p>
      </Card>

      <Card title="Default container labels">
        <KeyValueEditor rows={labels} onChange={(r) => (setLabels(r), setSaved(false))} keyPlaceholder="traefik.enable" valuePlaceholder="true" />
        <p className="mt-2 text-xs text-muted">
          Added to every new project's labels, e.g. to have a reverse proxy pick up {swarm ? 'services' : 'containers'} (Traefik,
          caddy-docker-proxy…).
        </p>
      </Card>

      <ErrorBox>{error}</ErrorBox>
      <div className="flex items-center justify-end gap-3">
        {saved && (
          <span className="inline-flex items-center gap-1.5 text-sm text-emerald-600 dark:text-emerald-400">
            <CheckCircle2 className="size-4" /> Saved
          </span>
        )}
        <Button type="submit" variant="primary" loading={saving}>
          Save settings
        </Button>
      </div>
    </form>
  )
}
