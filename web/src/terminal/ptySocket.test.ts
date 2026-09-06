import { encodeResize, parseControl, toBytes } from './ptySocket'

describe('encodeResize', () => {
  it('encodes the resize control frame', () => {
    expect(JSON.parse(encodeResize(120, 40))).toEqual({ type: 'resize', cols: 120, rows: 40 })
  })
  it('clamps to integers of at least 1', () => {
    expect(JSON.parse(encodeResize(0, 33.7))).toEqual({ type: 'resize', cols: 1, rows: 33 })
  })
})

describe('parseControl', () => {
  it('parses an exit frame with and without a code', () => {
    expect(parseControl('{"type":"exit"}')).toEqual({ type: 'exit' })
    expect(parseControl('{"type":"exit","code":137}')).toEqual({ type: 'exit', code: 137 })
  })
  it.each([['not json'], ['null'], ['42'], ['{"type":"resize","cols":1,"rows":1}'], ['{}']])(
    'returns null for %s',
    (input) => {
      expect(parseControl(input)).toBeNull()
    },
  )
})

describe('toBytes', () => {
  it('wraps an ArrayBuffer', () => {
    const buf = new Uint8Array([1, 2, 3]).buffer
    expect(Array.from(toBytes(buf))).toEqual([1, 2, 3])
  })
  it('respects a view offset', () => {
    const backing = new Uint8Array([9, 8, 7, 6])
    const view = new Uint8Array(backing.buffer, 1, 2)
    expect(Array.from(toBytes(view))).toEqual([8, 7])
  })
})
