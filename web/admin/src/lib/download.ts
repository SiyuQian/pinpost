/**
 * Hands the browser a generated file. Two details are load-bearing and both
 * were browser bugs rather than choices: Firefox only starts a download for an
 * anchor that is in the document, and revoking the object URL in the same task
 * can abort or truncate the transfer there and in older Safari.
 */
export function downloadBlob(blob: Blob, filename: string) {
  const url = URL.createObjectURL(blob)
  const link = document.createElement("a")
  link.href = url
  link.download = filename
  document.body.appendChild(link)
  link.click()
  link.remove()
  setTimeout(() => URL.revokeObjectURL(url), 0)
  return url
}
