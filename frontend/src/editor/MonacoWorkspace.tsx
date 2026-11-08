import Editor, { type Monaco, type OnMount } from '@monaco-editor/react'
import type { editor } from 'monaco-editor'

interface MonacoWorkspaceProps {
  value: string
  language: string
  readOnly: boolean
  onChange: (value: string) => void
  onMount?: (editor: editor.IStandaloneCodeEditor, monaco: Monaco) => void
}

export function MonacoWorkspace({ value, language, readOnly, onChange, onMount }: MonacoWorkspaceProps) {
  const mount: OnMount = (instance, monaco) => {
    instance.focus()
    onMount?.(instance, monaco)
  }

  return (
    <Editor
      height="100%"
      language={language}
      value={value}
      theme="vs-dark"
      onChange={(next) => {
        const nextValue = next ?? ''
        // Monaco also reports controlled value updates. Only user changes should
        // be converted into collaborative operations.
        if (nextValue !== value) onChange(nextValue)
      }}
      onMount={mount}
      loading={<div className="editor-loading"><span className="spinner" /> Loading Monaco…</div>}
      options={{
        readOnly,
        minimap: { enabled: true, scale: 0.8 },
        fontFamily: "'JetBrains Mono', 'SFMono-Regular', Consolas, monospace",
        fontSize: 14,
        lineHeight: 23,
        padding: { top: 18 },
        smoothScrolling: true,
        cursorSmoothCaretAnimation: 'on',
        renderLineHighlight: 'gutter',
        bracketPairColorization: { enabled: true },
        automaticLayout: true,
        scrollBeyondLastLine: false,
        wordWrap: 'off',
      }}
    />
  )
}
