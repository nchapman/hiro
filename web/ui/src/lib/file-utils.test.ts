import { describe, it, expect } from "vitest"
import { getFileExt, getPreviewType, isImageType } from "./file-utils"

// Only test the pure sync helpers here. The async API functions
// (listDir, readFile, etc.) are integration-level — they hit fetch
// against a real or mocked server and are better covered by e2e tests.

describe("getFileExt", () => {
  it("extracts extension from simple filename", () => {
    expect(getFileExt("photo.png")).toBe("png")
  })

  it("extracts extension from full path", () => {
    expect(getFileExt("/home/user/docs/file.txt")).toBe("txt")
  })

  it("lowercases extension", () => {
    expect(getFileExt("image.PNG")).toBe("png")
    expect(getFileExt("doc.Md")).toBe("md")
  })

  it("returns last extension for double extensions", () => {
    expect(getFileExt("archive.tar.gz")).toBe("gz")
  })

  it("returns full lowercased name for files without a dot", () => {
    expect(getFileExt("Makefile")).toBe("makefile")
  })

  it("returns empty string for empty path", () => {
    expect(getFileExt("")).toBe("")
  })
})

describe("getPreviewType", () => {
  it("identifies image files", () => {
    expect(getPreviewType("photo.png")).toBe("image")
    expect(getPreviewType("photo.jpg")).toBe("image")
    expect(getPreviewType("photo.jpeg")).toBe("image")
    expect(getPreviewType("photo.gif")).toBe("image")
    expect(getPreviewType("photo.webp")).toBe("image")
    expect(getPreviewType("photo.svg")).toBe("image")
    expect(getPreviewType("photo.bmp")).toBe("image")
    expect(getPreviewType("photo.ico")).toBe("image")
  })

  it("identifies video files", () => {
    expect(getPreviewType("clip.mp4")).toBe("video")
    expect(getPreviewType("clip.webm")).toBe("video")
    expect(getPreviewType("clip.ogg")).toBe("video")
  })

  it("identifies audio files", () => {
    expect(getPreviewType("song.mp3")).toBe("audio")
    expect(getPreviewType("song.wav")).toBe("audio")
    expect(getPreviewType("song.flac")).toBe("audio")
  })

  it("identifies PDF files", () => {
    expect(getPreviewType("doc.pdf")).toBe("pdf")
  })

  it("returns null for non-previewable files", () => {
    expect(getPreviewType("code.ts")).toBeNull()
    expect(getPreviewType("readme.md")).toBeNull()
    expect(getPreviewType("data.json")).toBeNull()
  })
})

describe("isImageType", () => {
  it("returns true for supported image MIME types", () => {
    expect(isImageType("image/jpeg")).toBe(true)
    expect(isImageType("image/png")).toBe(true)
    expect(isImageType("image/gif")).toBe(true)
    expect(isImageType("image/webp")).toBe(true)
  })

  it("returns false for non-image MIME types", () => {
    expect(isImageType("text/plain")).toBe(false)
    expect(isImageType("application/json")).toBe(false)
    expect(isImageType("application/pdf")).toBe(false)
    expect(isImageType("")).toBe(false)
  })

  it("returns false for unsupported image MIME types", () => {
    expect(isImageType("image/svg+xml")).toBe(false)
    expect(isImageType("image/tiff")).toBe(false)
    expect(isImageType("image/bmp")).toBe(false)
  })
})
