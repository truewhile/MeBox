import assert from 'node:assert/strict'
import test from 'node:test'

import { createLibraryCache } from './libraryCacheState.ts'
import type { Library } from '../types/library.ts'

const library = (id: string) => ({ id, name: id } as Library)

function deferred<T>() {
  let resolve!: (value: T) => void
  const promise = new Promise<T>((done) => { resolve = done })
  return { promise, resolve }
}

test('list requests share a flight and populate the detail cache', async () => {
  const request = deferred<Library[]>()
  let calls = 0
  const cache = createLibraryCache({
    readScope: () => 'account-a:profile-a',
    list: () => { calls++; return request.promise },
    get: async () => { throw new Error('detail should be cached') },
  })
  const first = cache.fetchLibraries()
  assert.equal(first, cache.fetchLibraries())
  request.resolve([library('a')])
  await first
  assert.equal((await cache.resolveLibrary('a')).id, 'a')
  assert.equal((await cache.fetchLibraries())[0].id, 'a')
  assert.equal(calls, 1)
})

test('account and profile changes discard both list and detail data', async () => {
  let scope = 'account-a:profile-a'
  const cache = createLibraryCache({
    readScope: () => scope,
    list: async () => [library(scope)],
    get: async () => library(scope),
  })
  await cache.fetchLibraries()
  scope = 'account-b:profile-a'
  assert.equal(cache.peekLibraries(), null)
  assert.equal(cache.peekLibrary('account-a:profile-a'), null)
  assert.equal((await cache.fetchLibraries())[0].id, scope)
  scope = 'account-b:profile-b'
  assert.equal(cache.peekLibraries(), null)
  assert.equal((await cache.resolveLibrary('shared-id')).id, scope)
})

test('invalidation rejects old list results without clearing the new flight', async () => {
  const old = deferred<Library[]>()
  const fresh = deferred<Library[]>()
  let calls = 0
  const cache = createLibraryCache({
    readScope: () => 'a',
    list: () => ++calls === 1 ? old.promise : fresh.promise,
    get: async (id) => library(id),
  })
  const oldRequest = cache.fetchLibraries()
  const rejected = assert.rejects(oldRequest, { name: 'AbortError' })
  cache.invalidate()
  const current = cache.fetchLibraries()
  old.resolve([library('deleted')])
  await rejected
  assert.equal(cache.peekLibraries(), null)
  assert.equal(cache.fetchLibraries(), current)
  fresh.resolve([library('current')])
  await current
  assert.equal(cache.peekLibraries()?.[0].id, 'current')
  assert.equal(calls, 2)
})

test('invalidation rejects old detail results without clearing the new flight', async () => {
  const old = deferred<Library>()
  const fresh = deferred<Library>()
  let calls = 0
  const cache = createLibraryCache({
    readScope: () => 'a',
    list: async () => [],
    get: () => ++calls === 1 ? old.promise : fresh.promise,
  })
  const oldRequest = cache.resolveLibrary('same-id')
  const rejected = assert.rejects(oldRequest, { name: 'AbortError' })
  cache.invalidate()
  const current = cache.resolveLibrary('same-id')
  old.resolve(library('old'))
  await rejected
  assert.equal(cache.peekLibrary('same-id'), null)
  assert.equal(cache.resolveLibrary('same-id'), current)
  fresh.resolve(library('current'))
  await current
  assert.equal(cache.peekLibrary('same-id')?.id, 'current')
})

test('logout followed by login to the same account invalidates old requests', async () => {
  let scope = 'account-a'
  const request = deferred<Library[]>()
  const cache = createLibraryCache({ readScope: () => scope, list: () => request.promise, get: async libraryID => library(libraryID) })
  const pending = cache.fetchLibraries()
  const rejected = assert.rejects(pending, { name: 'AbortError' })
  scope = 'logged-out'
  cache.syncScope()
  scope = 'account-a'
  cache.syncScope()
  request.resolve([library('old-session')])
  await rejected
  assert.equal(cache.peekLibraries(), null)
})
