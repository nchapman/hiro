import { describe, it, expect, vi, beforeEach } from "vitest"
import { listDir, readFile, writeFile, mkdir, deleteEntry, shareFile, renameEntry, uploadFile } from "@/hooks/use-files"

// Mock global fetch for API function tests
const mockFetch = vi.fn()
beforeEach(() => {
  vi.stubGlobal("fetch", mockFetch)
  mockFetch.mockReset()
})

describe("listDir", () => {
  it("fetches tree without path param", async () => {
    mockFetch.mockResolvedValue({ ok: true, json: () => Promise.resolve([]) })
    const result = await listDir()
    expect(mockFetch).toHaveBeenCalledWith("/api/files/tree")
    expect(result).toEqual([])
  })

  it("fetches tree with path param", async () => {
    mockFetch.mockResolvedValue({ ok: true, json: () => Promise.resolve([{ name: "a", path: "/a", type: "file" }]) })
    await listDir("/some/dir")
    expect(mockFetch).toHaveBeenCalledWith("/api/files/tree?path=%2Fsome%2Fdir")
  })

  it("throws on non-OK response", async () => {
    mockFetch.mockResolvedValue({ ok: false, statusText: "Not Found" })
    await expect(listDir("/bad")).rejects.toThrow("Failed to list directory: Not Found")
  })
})

describe("readFile", () => {
  it("fetches file content as text", async () => {
    mockFetch.mockResolvedValue({ ok: true, status: 200, text: () => Promise.resolve("file content") })
    const result = await readFile("/test.txt")
    expect(result).toBe("file content")
    expect(mockFetch).toHaveBeenCalledWith("/api/files/file?path=%2Ftest.txt")
  })

  it("throws specific message for 413", async () => {
    mockFetch.mockResolvedValue({ ok: false, status: 413, statusText: "Payload Too Large" })
    await expect(readFile("/big.bin")).rejects.toThrow("File too large to display")
  })
})

describe("writeFile", () => {
  it("sends PUT with content", async () => {
    mockFetch.mockResolvedValue({ ok: true })
    await writeFile("/test.txt", "hello")
    expect(mockFetch).toHaveBeenCalledWith(
      "/api/files/file?path=%2Ftest.txt",
      expect.objectContaining({
        method: "PUT",
        headers: { "Content-Type": "text/plain" },
        body: "hello",
      }),
    )
  })

  it("throws on non-OK response", async () => {
    mockFetch.mockResolvedValue({ ok: false, statusText: "Internal Server Error" })
    await expect(writeFile("/test.txt", "x")).rejects.toThrow("Failed to save file")
  })
})

describe("mkdir", () => {
  it("sends POST to mkdir endpoint", async () => {
    mockFetch.mockResolvedValue({ ok: true })
    await mkdir("/new/dir")
    expect(mockFetch).toHaveBeenCalledWith(
      "/api/files/mkdir?path=%2Fnew%2Fdir",
      { method: "POST" },
    )
  })

  it("throws on non-OK response", async () => {
    mockFetch.mockResolvedValue({ ok: false, statusText: "Forbidden" })
    await expect(mkdir("/root")).rejects.toThrow("Failed to create directory")
  })
})

describe("deleteEntry", () => {
  it("sends DELETE request", async () => {
    mockFetch.mockResolvedValue({ ok: true })
    await deleteEntry("/old.txt")
    expect(mockFetch).toHaveBeenCalledWith(
      "/api/files/file?path=%2Fold.txt",
      { method: "DELETE" },
    )
  })

  it("throws on non-OK response", async () => {
    mockFetch.mockResolvedValue({ ok: false, statusText: "Not Found" })
    await expect(deleteEntry("/missing")).rejects.toThrow("Failed to delete")
  })
})

describe("shareFile", () => {
  it("returns token on success", async () => {
    mockFetch.mockResolvedValue({
      ok: true,
      json: () => Promise.resolve({ token: "abc123" }),
    })
    const token = await shareFile("/test.txt")
    expect(token).toBe("abc123")
  })

  it("throws on non-OK response", async () => {
    mockFetch.mockResolvedValue({ ok: false, statusText: "Forbidden" })
    await expect(shareFile("/test.txt")).rejects.toThrow("Failed to create share link: Forbidden")
  })

  it("throws on missing token", async () => {
    mockFetch.mockResolvedValue({
      ok: true,
      json: () => Promise.resolve({}),
    })
    await expect(shareFile("/test.txt")).rejects.toThrow("Invalid share response")
  })
})

describe("renameEntry", () => {
  it("sends POST with from/to params", async () => {
    mockFetch.mockResolvedValue({ ok: true })
    await renameEntry("/old.txt", "/new.txt")
    expect(mockFetch).toHaveBeenCalledWith(
      "/api/files/rename?from=%2Fold.txt&to=%2Fnew.txt",
      { method: "POST" },
    )
  })

  it("throws specific message for 409 conflict", async () => {
    mockFetch.mockResolvedValue({ ok: false, status: 409, statusText: "Conflict" })
    await expect(renameEntry("/a", "/b")).rejects.toThrow("Destination already exists")
  })
})

describe("uploadFile", () => {
  it("sends PUT with file body and content type", async () => {
    mockFetch.mockResolvedValue({ ok: true, status: 200 })
    const file = new File(["data"], "test.png", { type: "image/png" })
    await uploadFile("/uploads/test.png", file)
    expect(mockFetch).toHaveBeenCalledWith(
      "/api/files/file?path=%2Fuploads%2Ftest.png",
      expect.objectContaining({
        method: "PUT",
        headers: { "Content-Type": "image/png" },
        body: file,
      }),
    )
  })

  it("throws for 413 status", async () => {
    mockFetch.mockResolvedValue({ ok: false, status: 413, statusText: "Payload Too Large" })
    const file = new File(["x".repeat(100)], "big.zip")
    await expect(uploadFile("/big.zip", file)).rejects.toThrow("File too large (max 50 MB)")
  })
})
