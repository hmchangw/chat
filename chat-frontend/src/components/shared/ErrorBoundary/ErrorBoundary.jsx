import { Component } from 'react'
import './style.css'

/**
 * Catches errors thrown during render in the subtree and shows a
 * recovery UI instead of letting React unmount everything to a white
 * screen. The chat client runs for hours per session; one stray null
 * deref shouldn't lose the user's context.
 *
 * Caveats (React's own rules):
 *   - Does NOT catch errors in event handlers, async code, or effects
 *     that fire outside a render phase. Those still surface as
 *     unhandledrejection / window.error. Add a global listener at the
 *     entry point if you want to capture those.
 *   - Does NOT catch errors during SSR (n/a — this is a SPA).
 *
 * Pass `fallback` to render a custom recovery UI; the default is a
 * full-viewport message with a Reload button.
 */
export default class ErrorBoundary extends Component {
  constructor(props) {
    super(props)
    this.state = { error: null }
  }

  static getDerivedStateFromError(error) {
    return { error }
  }

  componentDidCatch(error, info) {
    // Surface to the console with enough context for a triage. In prod
    // we'd ship to an error tracker here (Sentry / similar).
    // eslint-disable-next-line no-console
    console.error('[ErrorBoundary] render error:', error, info?.componentStack)
  }

  handleReload = () => {
    window.location.reload()
  }

  render() {
    if (!this.state.error) return this.props.children
    if (this.props.fallback) {
      return typeof this.props.fallback === 'function'
        ? this.props.fallback({ error: this.state.error, reload: this.handleReload })
        : this.props.fallback
    }
    return (
      <div className="error-boundary" role="alert">
        <div className="error-boundary-card">
          <h1 className="error-boundary-title">Something went wrong</h1>
          <p className="error-boundary-message">
            The app hit an unexpected error. Reloading the page usually fixes it.
          </p>
          {this.state.error?.message && (
            <pre className="error-boundary-detail">{this.state.error.message}</pre>
          )}
          <button
            type="button"
            className="btn btn-primary"
            onClick={this.handleReload}
            autoFocus
          >
            Reload
          </button>
        </div>
      </div>
    )
  }
}
