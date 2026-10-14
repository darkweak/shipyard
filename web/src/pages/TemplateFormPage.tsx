import { Copy, Plus, Rocket, Trash2 } from 'lucide-react'
import { useEffect, useMemo, useState, type FormEvent } from 'react'
import { api, type TemplateInput, type Variable } from '@/lib/api'
import { useAsync } from '@/lib/hooks'
import { navigate } from '@/lib/router'
import { Button, Card, CodeArea, ErrorBox, Field, Input, PageHeader, Spinner } from '@/components/ui'
import { Preview } from './ProjectFormPage'

const starter: TemplateInput = {
  name: '',
  description: '',
  defaultPort: 8080,
  variables: [{ name: 'BASE_IMAGE', default: 'alpine:3', description: 'Base image' }],
  dockerfile: `# syntax=docker/dockerfile:1
FROM {{ .BASE_IMAGE }}
WORKDIR /app
COPY . .
EXPOSE 8080
CMD ["./start.sh"]
`,
  dockerignore: '.git\n',
}

export function TemplateFormPage({ id }: { id?: string }) {
  const editing = !!id
  const { data: existing, error: loadError } = useAsync(() => (id ? api.template(id) : Promise.resolve(undefined)), [id])
  const [form, setForm] = useState<TemplateInput>(starter)
  const [error, setError] = useState('')
  const [busy, setBusy] = useState('')
  const [preview, setPreview] = useState<{ dockerfile?: string; error?: string }>({})

  useEffect(() => {
    if (existing) {
      const { name, description, dockerfile, dockerignore, variables, defaultPort } = existing
      setForm({ name, description, dockerfile, dockerignore, variables, defaultPort })
    }
  }, [existing])

  const set = <K extends keyof TemplateInput>(k: K, v: TemplateInput[K]) => setForm((f) => ({ ...f, [k]: v }))
  const setVar = (i: number, patch: Partial<Variable>) => set('variables', form.variables.map((v, j) => (j === i ? { ...v, ...patch } : v)))

  // Live preview with the default values.
  const previewKey = useMemo(() => JSON.stringify([form.dockerfile, form.variables]), [form.dockerfile, form.variables])
  useEffect(() => {
    const t = setTimeout(() => {
      api.preview({ dockerfile: form.dockerfile, variables: form.variables.filter((v) => v.name), values: {} }).then(setPreview, (e) => setPreview({ error: e.message }))
    }, 300)
    return () => clearTimeout(t)
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [previewKey])

  const run = async (name: string, fn: () => Promise<void>) => {
    setBusy(name)
    setError('')
    try {
      await fn()
    } catch (e) {
      setError((e as Error).message)
      setBusy('')
    }
  }

  const submit = (e: FormEvent) => {
    e.preventDefault()
    const body = { ...form, variables: form.variables.filter((v) => v.name.trim()) }
    run('save', async () => {
      const t = editing ? await api.updateTemplate(id!, body) : await api.createTemplate(body)
      if (editing) {
        setBusy('')
        navigate('/templates')
      } else navigate(`/templates/${t.id}`, { replace: true })
    })
  }

  if (editing && !existing) return loadError ? <ErrorBox>{loadError}</ErrorBox> : <Spinner />

  return (
    <form onSubmit={submit} className="space-y-5">
      <PageHeader
        title={editing ? existing!.name : 'New template'}
        subtitle={editing && existing!.builtin ? 'Built-in template — edits apply to every project using it on their next deploy.' : 'Changes apply to projects on their next deploy.'}
        actions={
          editing && (
            <>
              <Button type="button" icon={<Rocket className="size-4" />} onClick={() => navigate(`/projects/new?template=${id}`)}>
                Use in new project
              </Button>
              <Button
                type="button"
                icon={<Copy className="size-4" />}
                loading={busy === 'dup'}
                onClick={() =>
                  run('dup', async () => {
                    const t = await api.createTemplate({ ...form, name: `${form.name} (copy)` })
                    setBusy('')
                    navigate(`/templates/${t.id}`)
                  })
                }
              >
                Duplicate
              </Button>
              <Button
                type="button"
                variant="danger"
                icon={<Trash2 className="size-4" />}
                loading={busy === 'delete'}
                onClick={() =>
                  confirm(`Delete template ${form.name}?`) &&
                  run('delete', async () => {
                    await api.deleteTemplate(id!)
                    navigate('/templates')
                  })
                }
              >
                Delete
              </Button>
            </>
          )
        }
      />

      <Card title="Details">
        <div className="grid gap-4 sm:grid-cols-[2fr_1fr]">
          <Field label="Name">
            <Input required value={form.name} onChange={(e) => set('name', e.target.value)} placeholder="Astro site" />
          </Field>
          <Field label="Default container port" hint="Used when a project doesn't set one.">
            <Input type="number" min={0} max={65535} value={form.defaultPort || ''} onChange={(e) => set('defaultPort', Number(e.target.value))} />
          </Field>
          <Field label="Description" className="sm:col-span-2">
            <Input value={form.description} onChange={(e) => set('description', e.target.value)} placeholder="What kind of repository this builds" />
          </Field>
        </div>
      </Card>

      <Card
        title="Variables"
        actions={
          <Button type="button" size="sm" icon={<Plus className="size-3.5" />} onClick={() => set('variables', [...form.variables, { name: '', default: '', description: '' }])}>
            Add variable
          </Button>
        }
      >
        {form.variables.length === 0 ? (
          <p className="text-sm text-muted">No variables. Add some to let projects customise this template.</p>
        ) : (
          <div className="space-y-2">
            <div className="hidden grid-cols-[1fr_1fr_2fr_auto] gap-2 text-xs font-medium text-muted sm:grid">
              <span>Name</span>
              <span>Default</span>
              <span>Description</span>
              <span className="w-8" />
            </div>
            {form.variables.map((v, i) => (
              <div key={i} className="grid gap-2 sm:grid-cols-[1fr_1fr_2fr_auto]">
                <Input className="font-mono text-xs" placeholder="NAME" value={v.name} onChange={(e) => setVar(i, { name: e.target.value.replace(/[^A-Za-z0-9_]/g, '_') })} />
                <Input className="font-mono text-xs" placeholder="default" value={v.default} onChange={(e) => setVar(i, { default: e.target.value })} />
                <Input className="text-xs" placeholder="Shown to project owners" value={v.description} onChange={(e) => setVar(i, { description: e.target.value })} />
                <Button type="button" variant="ghost" className="px-2" aria-label="Remove variable" onClick={() => set('variables', form.variables.filter((_, j) => j !== i))}>
                  <Trash2 className="size-4" />
                </Button>
              </div>
            ))}
          </div>
        )}
      </Card>

      <Card title="Dockerfile">
        <div className="grid gap-4 lg:grid-cols-2">
          <div className="min-w-0 space-y-1.5">
            <p className="text-sm font-medium">Template</p>
            <CodeArea rows={22} value={form.dockerfile} onChange={(v) => set('dockerfile', v)} />
            <p className="text-xs text-muted">
              Insert variables with <code className="font-mono">{'{{ .NAME }}'}</code>. Conditionals work too:{' '}
              <code className="font-mono">{'{{ if .BUILD_COMMAND }}RUN {{ .BUILD_COMMAND }}{{ end }}'}</code>
            </p>
          </div>
          <Preview>{preview.error ? <span className="text-red-400">{preview.error}</span> : (preview.dockerfile ?? '…')}</Preview>
        </div>
      </Card>

      <Card title=".dockerignore">
        <CodeArea rows={6} value={form.dockerignore} onChange={(v) => set('dockerignore', v)} placeholder={'node_modules\n.git'} />
        <p className="mt-1.5 text-xs text-muted">Written into the build context only when the repository has no .dockerignore of its own.</p>
      </Card>

      <ErrorBox>{error}</ErrorBox>
      <div className="flex justify-end gap-2">
        <Button type="button" variant="ghost" onClick={() => navigate('/templates')}>
          Cancel
        </Button>
        <Button variant="primary" loading={busy === 'save'}>
          {editing ? 'Save template' : 'Create template'}
        </Button>
      </div>
    </form>
  )
}
