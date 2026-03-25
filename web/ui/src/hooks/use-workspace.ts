export interface FileEntry {
  name: string
  path: string
  type: "file" | "dir"
  size?: number
}

export async function listDir(path?: string): Promise<FileEntry[]> {
  const url = path
    ? `/api/workspace/tree?path=${encodeURIComponent(path)}`
    : "/api/workspace/tree"
  const res = await fetch(url)
  if (!res.ok) throw new Error(`Failed to list directory: ${res.statusText}`)
  return res.json()
}

export async function readFile(path: string): Promise<string> {
  const res = await fetch(
    `/api/workspace/file?path=${encodeURIComponent(path)}`
  )
  if (res.status === 413) throw new Error("File too large to display")
  if (!res.ok) throw new Error(`Failed to read file: ${res.statusText}`)
  return res.text()
}

export async function writeFile(
  path: string,
  content: string
): Promise<void> {
  const res = await fetch(
    `/api/workspace/file?path=${encodeURIComponent(path)}`,
    {
      method: "PUT",
      headers: { "Content-Type": "text/plain" },
      body: content,
    }
  )
  if (!res.ok) throw new Error(`Failed to save file: ${res.statusText}`)
}
