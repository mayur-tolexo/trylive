import { formatRemaining } from './Countdown'

describe('formatRemaining', () => {
  it('formats mm:ss and clamps at zero', () => {
    expect(formatRemaining(15 * 60_000)).toBe('15:00')
    expect(formatRemaining(61_500)).toBe('01:01')
    expect(formatRemaining(-5)).toBe('00:00')
  })
})
