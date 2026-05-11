import { useState, useRef, useEffect } from 'react'

/**
 * Controlled debounced-search hook.
 *
 * @param {Object}   opts
 * @param {number}   [opts.delay=250]   Debounce delay in ms.
 * @param {number}   [opts.minLen=2]    Minimum query length before fetcher is invoked.
 * @param {Function} [opts.fetcher]     `(q) => Promise<Result[]>`. Optional — when
 *                                      omitted/null, the hook is a controlled input
 *                                      tracker only: `query` updates, but no timer is
 *                                      scheduled and `results` stays `[]`. Use this
 *                                      mode for chip-input fields that don't have a
 *                                      server-side search endpoint yet.
 */
export function useDebouncedSearch({ delay = 250, minLen = 2, fetcher }) {
  const [query, setQuery] = useState('')
  const [results, setResults] = useState([])
  const debounceRef = useRef(null)

  useEffect(() => () => clearTimeout(debounceRef.current), [])

  const onChange = (q) => {
    setQuery(q)
    clearTimeout(debounceRef.current)
    if (q.length < minLen || !fetcher) {
      // Avoid no-op renders: `setState` to the same primitive bails out, but
      // `setResults([])` allocates a fresh array every call and would otherwise
      // re-render once per keystroke on fields with no fetcher.
      setResults((prev) => (prev.length === 0 ? prev : []))
      return
    }
    debounceRef.current = setTimeout(async () => {
      try {
        const resp = await fetcher(q)
        setResults(resp ?? [])
      } catch {
        setResults([])
      }
    }, delay)
  }

  const reset = () => {
    setQuery((prev) => (prev === '' ? prev : ''))
    setResults((prev) => (prev.length === 0 ? prev : []))
    clearTimeout(debounceRef.current)
  }

  return { query, results, onChange, reset }
}
