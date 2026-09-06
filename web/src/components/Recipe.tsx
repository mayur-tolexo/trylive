import type { Recipe as RecipeT } from '../api/types'

/** Recipe lists how the repo was run: detector, install steps, start command, port. */
export function Recipe({ recipe }: { recipe?: RecipeT }) {
  if (!recipe) return <p className="muted">Recipe not available yet.</p>
  return (
    <dl className="recipe">
      <dt>detector</dt>
      <dd className="mono">{recipe.detector}</dd>
      <dt>kind</dt>
      <dd className="mono">{recipe.kind}</dd>
      {recipe.cwd && recipe.cwd !== '.' && (
        <>
          <dt>cwd</dt>
          <dd className="mono">{recipe.cwd}</dd>
        </>
      )}
      <dt>install</dt>
      <dd>
        {recipe.install.length === 0 ? (
          <span className="muted">none</span>
        ) : (
          <ul className="recipe-cmds mono">
            {recipe.install.map((c, i) => (
              <li key={i}>
                <span className="muted">$ </span>
                {c}
              </li>
            ))}
          </ul>
        )}
      </dd>
      <dt>start</dt>
      <dd className="mono">
        <span className="muted">$ </span>
        {recipe.start}
      </dd>
      {recipe.kind === 'web' && (
        <>
          <dt>port</dt>
          <dd className="mono">{recipe.port}</dd>
        </>
      )}
      {Object.keys(recipe.env ?? {}).length > 0 && (
        <>
          <dt>env</dt>
          <dd>
            <ul className="recipe-cmds mono">
              {Object.entries(recipe.env).map(([k, v]) => (
                <li key={k}>
                  {k}={v}
                </li>
              ))}
            </ul>
          </dd>
        </>
      )}
    </dl>
  )
}
