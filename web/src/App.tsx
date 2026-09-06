import { BrowserRouter, Navigate, Route, Routes } from 'react-router-dom'
import { Landing } from './pages/Landing'
import { Session } from './pages/Session'

/** App wires the two routes; anything else goes home. */
export function App() {
  return (
    <BrowserRouter>
      <Routes>
        <Route path="/" element={<Landing />} />
        <Route path="/gh/:owner/:repo" element={<Session />} />
        <Route path="*" element={<Navigate to="/" replace />} />
      </Routes>
    </BrowserRouter>
  )
}
