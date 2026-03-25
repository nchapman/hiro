export interface FileEntry {
  name: string
  path: string
  type: "file" | "dir"
  size?: number
}

export async function listDir(path?: string): Promise<FileEntry[]> {
  const url = path
    ? `/api/files/tree?path=${encodeURIComponent(path)}`
    : "/api/files/tree"
  const res = await fetch(url)
  if (!res.ok) throw new Error(`Failed to list directory: ${res.statusText}`)
  return res.json()
}

export async function readFile(path: string): Promise<string> {
  const res = await fetch(
    `/api/files/file?path=${encodeURIComponent(path)}`
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
    `/api/files/file?path=${encodeURIComponent(path)}`,
    {
      method: "PUT",
      headers: { "Content-Type": "text/plain" },
      body: content,
    }
  )
  if (!res.ok) throw new Error(`Failed to save file: ${res.statusText}`)
}

export async function mkdir(path: string): Promise<void> {
  const res = await fetch(
    `/api/files/mkdir?path=${encodeURIComponent(path)}`,
    { method: "POST" }
  )
  if (!res.ok) throw new Error(`Failed to create directory: ${res.statusText}`)
}

export async function deleteEntry(path: string): Promise<void> {
  const res = await fetch(
    `/api/files/file?path=${encodeURIComponent(path)}`,
    { method: "DELETE" }
  )
  if (!res.ok) throw new Error(`Failed to delete: ${res.statusText}`)
}

export async function uploadFile(
  path: string,
  file: File
): Promise<void> {
  const res = await fetch(
    `/api/files/file?path=${encodeURIComponent(path)}`,
    {
      method: "PUT",
      headers: {
        "Content-Type": file.type || "application/octet-stream",
      },
      body: file,
    }
  )
  if (res.status === 413) throw new Error("File too large (max 50 MB)")
  if (!res.ok) throw new Error(`Failed to upload file: ${res.statusText}`)
}

export async function renameEntry(
  from: string,
  to: string
): Promise<void> {
  const res = await fetch(
    `/api/files/rename?from=${encodeURIComponent(from)}&to=${encodeURIComponent(to)}`,
    { method: "POST" }
  )
  if (res.status === 409) throw new Error("Destination already exists")
  if (!res.ok) throw new Error(`Failed to rename: ${res.statusText}`)
}
