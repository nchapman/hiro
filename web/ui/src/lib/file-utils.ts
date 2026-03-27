export const previewableExtensions: Record<string, "image" | "video" | "audio" | "pdf"> = {
  png: "image", jpg: "image", jpeg: "image", gif: "image",
  bmp: "image", ico: "image", webp: "image", svg: "image",
  mp4: "video", webm: "video", ogg: "video",
  mp3: "audio", wav: "audio", flac: "audio",
  pdf: "pdf",
}

export function getFileExt(path: string): string {
  return path.split("/").pop()?.split(".").pop()?.toLowerCase() ?? ""
}

export function getPreviewType(path: string): "image" | "video" | "audio" | "pdf" | null {
  return previewableExtensions[getFileExt(path)] ?? null
}
