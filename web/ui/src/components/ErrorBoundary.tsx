import { Component } from "react"
import type { ReactNode, ErrorInfo } from "react"
import { Button } from "@/components/ui/button"

interface Props {
  children: ReactNode
  section?: string
}

interface State {
  error: Error | null
}

export default class ErrorBoundary extends Component<Props, State> {
  state: State = { error: null }

  static getDerivedStateFromError(error: Error): State {
    return { error }
  }

  componentDidCatch(error: Error, info: ErrorInfo) {
    console.error(`[ErrorBoundary${this.props.section ? `: ${this.props.section}` : ""}]`, error, info.componentStack)
  }

  render() {
    if (this.state.error) {
      return (
        <div className="flex flex-1 flex-col items-center justify-center gap-3 p-8 text-sm">
          <p className="font-medium text-destructive">
            Something went wrong{this.props.section ? ` in ${this.props.section}` : ""}.
          </p>
          <p className="max-w-md text-center text-muted-foreground">
            {this.state.error.message}
          </p>
          <Button
            variant="outline"
            size="sm"
            onClick={() => this.setState({ error: null })}
          >
            Try again
          </Button>
        </div>
      )
    }
    return this.props.children
  }
}
